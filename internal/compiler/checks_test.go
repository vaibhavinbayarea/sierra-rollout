package compiler

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sierra-rollout/internal/spec"
)

const header = "apiVersion: rollout/v1\nagent: a\nramper: { plugin: mock-ramper }\n"
const vx = "validations:\n  x: { plugin: http-health, params: { path: /h } }\n"
const stage0 = "stages:\n  - name: s\n    regions: [r]\n"

// expectedLine mirrors the documented fallback: the path's line, else its
// nearest recorded ancestor's, else 1.
func expectedLine(t *testing.T, src, path string) int {
	t.Helper()
	_, lines, diags := spec.Parse([]byte(src))
	require.Empty(t, diags)
	for p := path; p != ""; p = parent(p) {
		if l := lines.Line(p); l > 0 {
			return l
		}
	}
	return 1
}

func TestChecks(t *testing.T) {
	cases := []struct {
		name, src, path, msg string
	}{
		{"apiVersion", "apiVersion: rollout/v2\nagent: a\nramper: { plugin: mock-ramper }\n" + stage0, "apiVersion", "unsupported apiVersion"},
		{"missing agent", "apiVersion: rollout/v1\nramper: { plugin: mock-ramper }\n" + stage0, "agent", "required"},
		{"no ramper", "apiVersion: rollout/v1\nagent: a\n" + stage0, "stages[0]", "no ramper"},
		{"unknown plugin", header + "validations:\n  x: { plugin: nope }\n" + stage0, "validations.x.plugin", `unknown plugin "nope"`},
		{"wrong kind", header + "validations:\n  x: { plugin: log-notifier }\n" + stage0, "validations.x.plugin", "is a notifier, not a validator"},
		{"bad params", header + "validations:\n  x: { plugin: prometheus, params: { max: 1 } }\n" + stage0, "validations.x.params", "query"},
		{"steps not increasing", header + stage0 + "    steps: [10, 10, 100]\n", "stages[0].steps", "strictly increasing"},
		{"steps not ending at 100", header + stage0 + "    steps: [10, 50]\n", "stages[0].steps", "must end at 100"},
		{"steps out of range", header + stage0 + "    steps: [0, 100]\n", "stages[0].steps", "between 1 and 100"},
		{"duplicate stage", header + stage0 + "  - name: s\n    regions: [r]\n", "stages[1].name", "duplicate stage"},
		{"duplicate region", header + "stages:\n  - name: s\n    regions: [r, r]\n", "stages[0].regions[1]", "duplicate region"},
		{"duplicate customer", header + stage0 + "    waves:\n      - { name: w1, customers: [c] }\n      - { name: w2, customers: [c] }\n",
			"stages[0].waves[1].customers[0]", "duplicate customer"},
		{"undefined validation", header + stage0 + "    validate: [missing]\n", "stages[0].validate[0]", "undefined validation"},
		{"action after hold", header + "defaults:\n  on_failure: [hold, { notify: log-notifier }]\n" + stage0, "defaults.on_failure[1]", "after hold"},
		{"max_parallel 0", header + stage0 + "    waves:\n      - { name: w, customers: [c], max_parallel: 0 }\n", "stages[0].waves[0].max_parallel", ">= 1"},
		{"negative bake", header + "defaults:\n  bake: -1m\n" + stage0, "defaults.bake", "negative"},
		{"invalid region name", header + "stages:\n  - name: s\n    regions: [US_East]\n", "stages[0].regions[0]", "invalid name"},
		{"no regions", header + "stages:\n  - name: s\n", "stages[0].regions", "required"},
		{"bad severity", header + "validations:\n  x: { plugin: http-health, params: { path: /h }, severity: high }\n" + stage0, "validations.x.severity", "critical or warning"},
		{"bad when", header + "validations:\n  x: { plugin: http-health, params: { path: /h }, when: sometimes }\n" + stage0, "validations.x.when", "every_step or final_step"},
		{"bad warning", header + "defaults:\n  decision: { warning: shout }\n" + stage0, "defaults.decision.warning", "notify, ignore or fail"},
		{"bad on_timeout", header + "defaults:\n  on_failure: [{ pause: { timeout: 1h, on_timeout: retry } }]\n" + stage0, "defaults.on_failure[0].pause.on_timeout", "hold or abort"},
		{"notify with validator", header + "defaults:\n  on_failure: [{ notify: prometheus }]\n" + stage0, "defaults.on_failure[0].notify", "is a validator, not a notifier"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, diags := Compile([]byte(c.src), loadRegistry(t), "v1")
			assert.Nil(t, p)
			var match *spec.Diagnostic
			for i := range diags {
				if diags[i].Path == c.path && strings.Contains(diags[i].Message, c.msg) {
					match = &diags[i]
				}
			}
			require.NotNil(t, match, "want %s: %q, got %+v", c.path, c.msg, diags)
			assert.Equal(t, expectedLine(t, c.src, c.path), match.Line)
		})
	}
}

func TestChecksValidPolicy(t *testing.T) {
	_, diags := Compile([]byte(header+vx+stage0+"    validate: [x]\n"), loadRegistry(t), "v1")
	assert.Empty(t, diags)
}

func TestChecksAccumulate(t *testing.T) {
	src := header + "validations:\n  x: { plugin: nope }\n" + stage0 + "    steps: [10, 50]\n    validate: [missing]\n"
	_, diags := Compile([]byte(src), loadRegistry(t), "v1")
	require.Len(t, diags, 3)
	for i := 1; i < len(diags); i++ {
		assert.LessOrEqual(t, diags[i-1].Line, diags[i].Line)
	}
}
