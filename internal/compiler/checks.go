package compiler

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"sierra-rollout/internal/registry"
	"sierra-rollout/internal/spec"
)

// nameRe constrains names that become part of workflow IDs and URLs.
var nameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)

type checker struct {
	lines *spec.Lines
	reg   *registry.Registry
	diags []spec.Diagnostic
}

// check returns semantic diagnostics for a parsed policy, sorted by line.
func check(p *spec.Policy, lines *spec.Lines, reg *registry.Registry) []spec.Diagnostic {
	c := &checker{lines: lines, reg: reg}

	if p.APIVersion != "rollout/v1" {
		c.add("apiVersion", "unsupported apiVersion %q (want rollout/v1)", p.APIVersion)
	}
	if p.Agent == "" {
		c.add("agent", "agent is required")
	} else {
		c.name("agent", p.Agent)
	}
	if p.Ramper != nil {
		c.plugin("ramper", *p.Ramper, registry.KindRamper)
	}
	for _, name := range sortedKeys(p.Validations) {
		c.validation("validations."+name, p.Validations[name])
	}
	c.overrides("defaults", p.Defaults, p)

	if len(p.Stages) == 0 {
		c.add("stages", "at least one stage is required")
	}
	stages := map[string]bool{}
	for i, st := range p.Stages {
		sp := fmt.Sprintf("stages[%d]", i)
		if st.Name == "" {
			c.add(sp+".name", "stage name is required")
		} else if stages[st.Name] {
			c.add(sp+".name", "duplicate stage %q", st.Name)
		} else {
			stages[st.Name] = true
			c.name(sp+".name", st.Name)
		}
		c.overrides(sp, st.Overrides, p)
		c.waves(sp, st, p)
	}

	sort.SliceStable(c.diags, func(i, j int) bool {
		a, b := c.diags[i], c.diags[j]
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Path < b.Path
	})
	return c.diags
}

func (c *checker) waves(sp string, st spec.Stage, p *spec.Policy) {
	if len(st.Waves) == 0 {
		c.target(sp, sp, []spec.Overrides{st.Overrides, p.Defaults}, p)
		return
	}
	waveNames, customers := map[string]bool{}, map[string]bool{}
	for j, w := range st.Waves {
		wp := fmt.Sprintf("%s.waves[%d]", sp, j)
		if w.Name == "" {
			c.add(wp+".name", "wave name is required")
		} else if waveNames[w.Name] {
			c.add(wp+".name", "duplicate wave %q", w.Name)
		} else {
			waveNames[w.Name] = true
			c.name(wp+".name", w.Name)
		}
		if len(w.Customers) == 0 {
			c.add(wp+".customers", "at least one customer is required")
		}
		for k, cu := range w.Customers {
			cp := fmt.Sprintf("%s.customers[%d]", wp, k)
			if customers[cu] {
				c.add(cp, "duplicate customer %q in stage", cu)
				continue
			}
			customers[cu] = true
			c.name(cp, cu)
		}
		if w.MaxParallel != nil && *w.MaxParallel < 1 {
			c.add(wp+".max_parallel", "max_parallel must be >= 1")
		}
		c.overrides(wp, w.Overrides, p)
		c.target(wp, sp, []spec.Overrides{w.Overrides, st.Overrides, p.Defaults}, p)
	}
}

// target checks what only exists once levels are merged.
func (c *checker) target(path, stagePath string, levels []spec.Overrides, p *spec.Policy) {
	if first(levels, func(o spec.Overrides) *spec.PluginCall { return o.Ramper }) == nil && p.Ramper == nil {
		c.add(stagePath, "no ramper configured for %s", path)
	}
	if first(levels, func(o spec.Overrides) []string { return o.Regions }) == nil {
		c.add(path+".regions", "regions are required")
	}
}

func (c *checker) overrides(path string, o spec.Overrides, p *spec.Policy) {
	if o.Ramper != nil {
		c.plugin(path+".ramper", *o.Ramper, registry.KindRamper)
	}
	if o.Steps != nil {
		c.steps(path+".steps", o.Steps)
	}
	if o.Bake != nil && o.Bake.D() < 0 {
		c.add(path+".bake", "bake must not be negative")
	}
	if o.Decision != nil {
		c.decision(path+".decision", *o.Decision)
	}
	c.onFailure(path+".on_failure", o.OnFailure)
	seen := map[string]bool{}
	for i, r := range o.Regions {
		rp := fmt.Sprintf("%s.regions[%d]", path, i)
		if seen[r] {
			c.add(rp, "duplicate region %q", r)
			continue
		}
		seen[r] = true
		c.name(rp, r)
	}
	for i, v := range o.Validate {
		if _, ok := p.Validations[v]; !ok {
			c.add(fmt.Sprintf("%s.validate[%d]", path, i), "undefined validation %q", v)
		}
	}
}

func (c *checker) steps(path string, steps []int) {
	if len(steps) == 0 {
		c.add(path, "steps must not be empty")
		return
	}
	for i, s := range steps {
		if s < 1 || s > 100 {
			c.add(path, "steps must be between 1 and 100, got %d", s)
			return
		}
		if i > 0 && s <= steps[i-1] {
			c.add(path, "steps must be strictly increasing")
			return
		}
	}
	if steps[len(steps)-1] != 100 {
		c.add(path, "steps must end at 100")
	}
}

func (c *checker) decision(path string, d spec.Decision) {
	if d.Critical != "" && d.Critical != "all_pass" {
		c.add(path+".critical", "critical must be all_pass")
	}
	switch d.Warning {
	case "", "notify", "ignore", "fail":
	default:
		c.add(path+".warning", "warning must be notify, ignore or fail")
	}
	if in := d.Inconclusive; in != nil {
		if in.Retry < 0 {
			c.add(path+".inconclusive.retry", "retry must not be negative")
		}
		if in.Backoff.D() < 0 {
			c.add(path+".inconclusive.backoff", "backoff must not be negative")
		}
		switch in.Then {
		case "", "fail", "pass":
		default:
			c.add(path+".inconclusive.then", "then must be fail or pass")
		}
	}
}

func (c *checker) onFailure(path string, actions []spec.FailureAction) {
	held := false
	for i, a := range actions {
		ap := fmt.Sprintf("%s[%d]", path, i)
		if held {
			c.add(ap, "no action may follow hold (it ends the target) — action after hold")
		}
		switch {
		case a.Notify != "":
			c.kind(ap+".notify", a.Notify, registry.KindNotifier)
		case a.Retry != nil:
			if a.Retry.Max < 1 {
				c.add(ap+".retry.max", "max must be >= 1")
			}
			if a.Retry.Backoff.D() < 0 {
				c.add(ap+".retry.backoff", "backoff must not be negative")
			}
		case a.Pause != nil:
			if a.Pause.Timeout.D() < 0 {
				c.add(ap+".pause.timeout", "timeout must not be negative")
			}
			switch a.Pause.OnTimeout {
			case "", "hold", "abort":
			default:
				c.add(ap+".pause.on_timeout", "on_timeout must be hold or abort")
			}
		case a.Hold:
			held = true
		}
	}
}

func (c *checker) validation(path string, v spec.Validation) {
	if c.kind(path+".plugin", v.Plugin, registry.KindValidator) {
		if err := c.reg.ValidateParams(v.Plugin, v.Params); err != nil {
			c.add(path+".params", "invalid params: %s", flatten(err))
		}
	}
	switch v.Severity {
	case "", "critical", "warning":
	default:
		c.add(path+".severity", "severity must be critical or warning")
	}
	switch v.When {
	case "", "every_step", "final_step":
	default:
		c.add(path+".when", "when must be every_step or final_step")
	}
}

func (c *checker) plugin(path string, pc spec.PluginCall, want registry.Kind) {
	if c.kind(path+".plugin", pc.Plugin, want) {
		if err := c.reg.ValidateParams(pc.Plugin, pc.Params); err != nil {
			c.add(path+".params", "invalid params: %s", flatten(err))
		}
	}
}

// kind reports whether name is a registered plugin of the wanted kind.
func (c *checker) kind(path, name string, want registry.Kind) bool {
	e, ok := c.reg.Get(name)
	switch {
	case name == "":
		c.add(path, "plugin is required")
	case !ok:
		c.add(path, "unknown plugin %q", name)
	case e.Kind != want:
		c.add(path, "%q is a %s, not a %s", name, e.Kind, want)
	default:
		return true
	}
	return false
}

func (c *checker) name(path, n string) {
	if !nameRe.MatchString(n) {
		c.add(path, "invalid name %q (use lowercase letters, digits, '.', '_', '-')", n)
	}
}

func (c *checker) add(path, format string, args ...any) {
	c.diags = append(c.diags, spec.Diagnostic{Path: path, Line: c.lineFor(path), Message: fmt.Sprintf(format, args...)})
}

// lineFor returns the line of path, else its nearest recorded ancestor's,
// else 1.
func (c *checker) lineFor(path string) int {
	for p := path; p != ""; p = parent(p) {
		if l := c.lines.Line(p); l > 0 {
			return l
		}
	}
	return 1
}

// parent strips the last ".key" or "[i]" segment of a path.
func parent(p string) string {
	i := strings.LastIndexAny(p, ".[")
	if i < 0 {
		return ""
	}
	return p[:i]
}

func flatten(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
