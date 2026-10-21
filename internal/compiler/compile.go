// Package compiler turns a policy into an immutable plan.
package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/registry"
	"sierra-rollout/internal/spec"
)

var kindTimeouts = map[registry.Kind]time.Duration{
	registry.KindRamper:    5 * time.Minute,
	registry.KindValidator: 2 * time.Minute,
	registry.KindNotifier:  30 * time.Second,
}

// Compile parses, checks and resolves a policy. It returns either a plan or
// diagnostics, never both.
func Compile(policy []byte, reg *registry.Registry, version string) (*plan.Plan, []spec.Diagnostic) {
	p, lines, diags := spec.Parse(policy)
	if len(diags) > 0 {
		return nil, diags
	}
	if diags := check(p, lines, reg); len(diags) > 0 {
		return nil, diags
	}
	sum := sha256.Sum256(policy)
	out := &plan.Plan{
		ID:         plan.GlobalID(p.Agent, version, p.Concurrency.Pipelining),
		Agent:      p.Agent,
		Version:    version,
		PolicyHash: hex.EncodeToString(sum[:]),
		Pipelining: p.Concurrency.Pipelining,
		Plugins:    map[string]plan.PluginRef{},
	}
	for _, st := range p.Stages {
		out.Stages = append(out.Stages, resolveStage(p, st))
	}
	for _, t := range out.Targets() {
		use := func(name string) {
			e, _ := reg.Get(name)
			to := e.Timeout
			if to == 0 {
				to = kindTimeouts[e.Kind]
			}
			out.Plugins[name] = plan.PluginRef{Name: name, Kind: string(e.Kind), Builtin: e.Builtin, Endpoint: e.Endpoint, Timeout: to}
		}
		use(t.Ramper.Plugin)
		use(t.Decision.WarningNotifier)
		for _, v := range t.Validations {
			use(v.Plugin)
		}
		for _, a := range t.OnFailure {
			if a.Kind == "notify" {
				use(a.Notifier)
			}
		}
	}
	return out, nil
}

// waves returns the stage's waves, or one implicit customer-less wave.
func waves(st spec.Stage) []spec.Wave {
	if len(st.Waves) > 0 {
		return st.Waves
	}
	return []spec.Wave{{Name: "default", Customers: []string{"_"}}}
}

func resolveStage(p *spec.Policy, st spec.Stage) plan.StagePlan {
	sp := plan.StagePlan{Name: st.Name}
	for _, w := range waves(st) {
		levels := []spec.Overrides{w.Overrides, st.Overrides, p.Defaults}
		wp := plan.WavePlan{Name: w.Name, MaxParallel: 1}
		if w.MaxParallel != nil {
			wp.MaxParallel = *w.MaxParallel
		}
		for _, c := range w.Customers {
			cp := plan.CustomerPlan{Customer: c}
			for _, region := range first(levels, func(o spec.Overrides) []string { return o.Regions }) {
				cp.Targets = append(cp.Targets, resolveTarget(p, levels, st.Name, w.Name, c, region))
			}
			wp.Customers = append(wp.Customers, cp)
		}
		sp.Waves = append(sp.Waves, wp)
	}
	return sp
}

func resolveTarget(p *spec.Policy, levels []spec.Overrides, stage, wave, customer, region string) plan.TargetPlan {
	t := plan.TargetPlan{
		ID:    stage + "/" + wave + "/" + customer + "/" + region,
		Stage: stage, Wave: wave, Customer: customer, Region: region,
		Steps: []int{100},
	}
	if s := first(levels, func(o spec.Overrides) []int { return o.Steps }); s != nil {
		t.Steps = s
	}
	ramper := p.Ramper
	if r := first(levels, func(o spec.Overrides) *spec.PluginCall { return o.Ramper }); r != nil {
		ramper = r
	}
	if ramper != nil {
		t.Ramper = plan.PluginCall{Plugin: ramper.Plugin, Params: ramper.Params}
	}
	if b := first(levels, func(o spec.Overrides) *spec.Duration { return o.Bake }); b != nil {
		t.Bake = b.D()
	}
	for _, name := range first(levels, func(o spec.Overrides) []string { return o.Validate }) {
		v := p.Validations[name]
		t.Validations = append(t.Validations, plan.ValidationCall{
			Name: name, Plugin: v.Plugin, Params: v.Params,
			Severity: or(v.Severity, "critical"), When: or(v.When, "every_step"),
		})
	}
	t.OnFailure = resolveOnFailure(first(levels, func(o spec.Overrides) []spec.FailureAction { return o.OnFailure }))
	t.Decision = resolveDecision(levels)
	t.Decision.WarningNotifier = "log-notifier"
	for _, a := range t.OnFailure {
		if a.Kind == "notify" {
			t.Decision.WarningNotifier = a.Notifier
			break
		}
	}
	return t
}

func resolveOnFailure(actions []spec.FailureAction) []plan.FailureAction {
	if actions == nil {
		return []plan.FailureAction{{Kind: "pause", Timeout: 24 * time.Hour, OnTimeout: "hold"}}
	}
	out := make([]plan.FailureAction, 0, len(actions))
	for _, a := range actions {
		switch {
		case a.Notify != "":
			out = append(out, plan.FailureAction{Kind: "notify", Notifier: a.Notify})
		case a.Retry != nil:
			out = append(out, plan.FailureAction{Kind: "retry", Max: a.Retry.Max, Backoff: a.Retry.Backoff.D()})
		case a.Pause != nil:
			out = append(out, plan.FailureAction{Kind: "pause", Timeout: a.Pause.Timeout.D(), OnTimeout: or(a.Pause.OnTimeout, "hold")})
		case a.Hold:
			out = append(out, plan.FailureAction{Kind: "hold"})
		}
	}
	return out
}

// resolveDecision merges decision fields individually across levels.
func resolveDecision(levels []spec.Overrides) plan.DecisionRule {
	d := plan.DecisionRule{
		Critical:     "all_pass",
		Warning:      "notify",
		Inconclusive: plan.InconclusiveRule{Retry: 3, Backoff: time.Minute, Then: "fail"},
	}
	critical, warning := "", ""
	var inc *spec.Inconclusive
	for _, l := range levels {
		if l.Decision == nil {
			continue
		}
		critical = or(critical, l.Decision.Critical)
		warning = or(warning, l.Decision.Warning)
		if inc == nil {
			inc = l.Decision.Inconclusive
		}
	}
	d.Critical = or(critical, d.Critical)
	d.Warning = or(warning, d.Warning)
	if inc != nil {
		d.Inconclusive = plan.InconclusiveRule{Retry: inc.Retry, Backoff: inc.Backoff.D(), Then: or(inc.Then, "fail")}
	}
	return d
}

// first returns the first non-zero value of get across levels (most
// specific first).
func first[T any](levels []spec.Overrides, get func(spec.Overrides) T) T {
	var zero T
	for _, l := range levels {
		v := get(l)
		if !isZero(v) {
			return v
		}
	}
	return zero
}

func isZero(v any) bool {
	switch x := v.(type) {
	case []int:
		return x == nil
	case []string:
		return x == nil
	case []spec.FailureAction:
		return x == nil
	case *spec.PluginCall:
		return x == nil
	case *spec.Duration:
		return x == nil
	}
	return v == nil
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
