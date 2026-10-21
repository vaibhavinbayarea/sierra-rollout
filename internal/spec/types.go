// Package spec defines the rollout policy YAML schema and parses it with
// line information for diagnostics.
package spec

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Policy is the owner-authored rollout policy (apiVersion rollout/v1).
type Policy struct {
	APIVersion  string                `yaml:"apiVersion"`
	Agent       string                `yaml:"agent"`
	Ramper      *PluginCall           `yaml:"ramper"`
	Concurrency Concurrency           `yaml:"concurrency"`
	Defaults    Overrides             `yaml:"defaults"`
	Validations map[string]Validation `yaml:"validations"`
	Stages      []Stage               `yaml:"stages"`
}

type Concurrency struct {
	Pipelining bool `yaml:"pipelining"`
}

// PluginCall references a registry plugin with its parameters.
type PluginCall struct {
	Plugin string         `yaml:"plugin"`
	Params map[string]any `yaml:"params"`
}

// Overrides are the fields that resolve wave > stage > defaults > built-ins.
type Overrides struct {
	Ramper    *PluginCall     `yaml:"ramper"`
	Steps     []int           `yaml:"steps"`
	Bake      *Duration       `yaml:"bake"`
	Decision  *Decision       `yaml:"decision"`
	OnFailure []FailureAction `yaml:"on_failure"`
	Regions   []string        `yaml:"regions"`
	Validate  []string        `yaml:"validate"`
}

type Stage struct {
	Name      string `yaml:"name"`
	Overrides `yaml:",inline"`
	Waves     []Wave `yaml:"waves"`
	Dark      *Dark  `yaml:"dark"`
}

// Dark is a dark canary gate: mirrored traffic to every target of the
// stage, validated before the stage's live waves start.
type Dark struct {
	Mirror    []int           `yaml:"mirror"`
	Bake      *Duration       `yaml:"bake"`
	Validate  []string        `yaml:"validate"`
	Decision  *Decision       `yaml:"decision"`
	OnFailure []FailureAction `yaml:"on_failure"`
	Ramper    *PluginCall     `yaml:"ramper"`
}

// Overrides returns the dark block as an override level (mirror excluded).
func (d *Dark) Overrides() Overrides {
	return Overrides{Ramper: d.Ramper, Bake: d.Bake, Decision: d.Decision, OnFailure: d.OnFailure, Validate: d.Validate}
}

type Wave struct {
	Name      string   `yaml:"name"`
	Customers []string `yaml:"customers"`
	// MaxParallel is a pointer so an explicit 0 can be diagnosed.
	MaxParallel *int `yaml:"max_parallel"`
	Overrides   `yaml:",inline"`
}

type Validation struct {
	Plugin   string         `yaml:"plugin"`
	Params   map[string]any `yaml:"params"`
	Severity string         `yaml:"severity"`
	When     string         `yaml:"when"`
}

type Decision struct {
	Critical     string        `yaml:"critical"`
	Warning      string        `yaml:"warning"`
	Inconclusive *Inconclusive `yaml:"inconclusive"`
}

type Inconclusive struct {
	Retry   int      `yaml:"retry"`
	Backoff Duration `yaml:"backoff"`
	Then    string   `yaml:"then"`
}

// FailureAction is exactly one of notify, retry, pause or hold.
type FailureAction struct {
	Notify string       `yaml:"notify"`
	Retry  *RetryAction `yaml:"retry"`
	Pause  *PauseAction `yaml:"pause"`
	Hold   bool         `yaml:"hold"`
}

type RetryAction struct {
	Max     int      `yaml:"max"`
	Backoff Duration `yaml:"backoff"`
}

type PauseAction struct {
	Timeout   Duration `yaml:"timeout"`
	OnTimeout string   `yaml:"on_timeout"`
}

// UnmarshalYAML accepts the scalar `hold` or a single-key mapping.
func (f *FailureAction) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if n.Value != "hold" {
			return typeErr(n, "unknown failure action %q", n.Value)
		}
		*f = FailureAction{Hold: true}
		return nil
	}
	if n.Kind != yaml.MappingNode || len(n.Content) != 2 {
		return typeErr(n, "failure action must be `hold` or a single-key mapping")
	}
	switch k := n.Content[0].Value; k {
	case "notify", "retry", "pause", "hold":
	default:
		return typeErr(n.Content[0], "unknown failure action %q", k)
	}
	type plain FailureAction
	return n.Decode((*plain)(f))
}

func typeErr(n *yaml.Node, format string, args ...any) error {
	return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: %s", n.Line, fmt.Sprintf(format, args...))}}
}
