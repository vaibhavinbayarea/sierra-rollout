// Package plan defines the immutable, fully resolved rollout plan that is
// the only input to the rollout workflows.
package plan

import (
	"fmt"
	"time"
)

type Plan struct {
	ID         string               `json:"id"`
	Agent      string               `json:"agent"`
	Version    string               `json:"version"`
	PolicyHash string               `json:"policy_hash"`
	Pipelining bool                 `json:"pipelining"`
	Plugins    map[string]PluginRef `json:"plugins"`
	Stages     []StagePlan          `json:"stages"`
}

type StagePlan struct {
	Name  string     `json:"name"`
	Waves []WavePlan `json:"waves"`
}

type WavePlan struct {
	Name        string         `json:"name"`
	MaxParallel int            `json:"max_parallel"`
	Customers   []CustomerPlan `json:"customers"`
}

type CustomerPlan struct {
	Customer string       `json:"customer"`
	Targets  []TargetPlan `json:"targets"` // region order
}

type TargetPlan struct {
	ID          string           `json:"id"` // <stage>/<wave>/<customer|_>/<region>
	Stage       string           `json:"stage"`
	Wave        string           `json:"wave"`
	Customer    string           `json:"customer"`
	Region      string           `json:"region"`
	Ramper      PluginCall       `json:"ramper"`
	Steps       []int            `json:"steps"`
	Bake        time.Duration    `json:"bake"`
	Validations []ValidationCall `json:"validations"`
	Decision    DecisionRule     `json:"decision"`
	OnFailure   []FailureAction  `json:"on_failure"`
	Mode        string           `json:"mode,omitempty"` // "" (live) or "dark"
}

// PluginRef is a registry entry frozen into the plan.
type PluginRef struct {
	Name     string        `json:"name"`
	Kind     string        `json:"kind"`
	Builtin  bool          `json:"builtin"`
	Endpoint string        `json:"endpoint,omitempty"`
	Timeout  time.Duration `json:"timeout"`
	Modes    []string      `json:"modes,omitempty"` // ramper traffic modes
}

type PluginCall struct {
	Plugin string         `json:"plugin"`
	Params map[string]any `json:"params,omitempty"`
}

type ValidationCall struct {
	Name     string         `json:"name"`
	Plugin   string         `json:"plugin"`
	Params   map[string]any `json:"params,omitempty"`
	Severity string         `json:"severity"` // critical | warning
	When     string         `json:"when"`     // every_step | final_step
}

type DecisionRule struct {
	Critical        string           `json:"critical"`
	Warning         string           `json:"warning"`
	WarningNotifier string           `json:"warning_notifier"`
	Inconclusive    InconclusiveRule `json:"inconclusive"`
}

type InconclusiveRule struct {
	Retry   int           `json:"retry"`
	Backoff time.Duration `json:"backoff"`
	Then    string        `json:"then"` // fail | pass
}

type FailureAction struct {
	Kind      string        `json:"kind"` // notify | retry | pause | hold
	Notifier  string        `json:"notifier,omitempty"`
	Max       int           `json:"max,omitempty"`
	Backoff   time.Duration `json:"backoff,omitempty"`
	Timeout   time.Duration `json:"timeout,omitempty"`
	OnTimeout string        `json:"on_timeout,omitempty"`
}

type Outcome string

const (
	PASS         Outcome = "PASS"
	FAIL         Outcome = "FAIL"
	INCONCLUSIVE Outcome = "INCONCLUSIVE"
	AWAIT_SIGNAL Outcome = "AWAIT_SIGNAL"
)

type Verdict struct {
	Outcome  Outcome        `json:"outcome"`
	Summary  string         `json:"summary,omitempty"`
	Evidence map[string]any `json:"evidence,omitempty"`
}

// Targets returns every target in plan order.
func (p *Plan) Targets() []TargetPlan {
	var out []TargetPlan
	for _, s := range p.Stages {
		for _, w := range s.Waves {
			for _, c := range w.Customers {
				out = append(out, c.Targets...)
			}
		}
	}
	return out
}

// GlobalID is the GlobalRollout workflow ID.
func GlobalID(agent, version string, pipelining bool) string {
	if pipelining {
		return fmt.Sprintf("ro/%s/%s", agent, version)
	}
	return "ro/" + agent
}

// StageID is the StageRollout workflow ID; it doubles as the stage lock.
func StageID(agent, stage string) string {
	return fmt.Sprintf("ro/%s/stage/%s", agent, stage)
}

// TargetWorkflowID is the TargetRollout workflow ID.
func TargetWorkflowID(globalID string, t TargetPlan) string {
	return globalID + "/" + t.ID
}
