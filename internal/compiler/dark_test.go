package compiler

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/spec"
)

func TestDarkExpansion(t *testing.T) {
	p := compileFile(t, "dark.yaml")
	var names []string
	for _, s := range p.Stages {
		names = append(names, s.Name)
	}
	assert.Equal(t, []string{"staging", "prod-dark", "prod"}, names)

	dark := p.Stages[1]
	require.Len(t, dark.Waves, 1)
	w := dark.Waves[0]
	assert.Equal(t, "dark", w.Name)
	assert.Equal(t, 5, w.MaxParallel, "every dark target runs at once")
	var ids []string
	for _, c := range w.Customers {
		require.Len(t, c.Targets, 1)
		tg := c.Targets[0]
		ids = append(ids, tg.ID)
		assert.Equal(t, "dark", tg.Mode)
		assert.Equal(t, "prod-dark", tg.Stage)
		assert.Equal(t, []int{50, 100}, tg.Steps)
		assert.Equal(t, 10*time.Minute, tg.Bake, "stage-level bake; wave overrides do not apply")
		assert.Equal(t, "mock-ramper", tg.Ramper.Plugin)
		assert.Equal(t, []plan.FailureAction{{Kind: "hold"}}, tg.OnFailure)
		require.Len(t, tg.Validations, 1)
		assert.Equal(t, "diff", tg.Validations[0].Name)
	}
	assert.Equal(t, []string{
		"prod-dark/canary/acme/us-east-1",
		"prod-dark/broad/globex/us-east-1",
		"prod-dark/broad/globex/eu-west-1",
		"prod-dark/broad/initech/us-east-1",
		"prod-dark/broad/initech/eu-west-1",
	}, ids)
	for _, tg := range p.Stages[2].Waves[0].Customers[0].Targets {
		assert.Empty(t, tg.Mode, "live targets have no mode")
	}
	assert.Equal(t, []string{"live", "shadow"}, p.Plugins["mock-ramper"].Modes)
}

func TestDarkExpansionUsesWaveRegions(t *testing.T) {
	p := compileFile(t, "dark.yaml")
	for _, c := range p.Stages[1].Waves[0].Customers {
		if c.Customer == "acme" {
			assert.Equal(t, "us-east-1", c.Targets[0].Region, "canary wave only covers us-east-1")
		}
	}
	assert.Len(t, p.Stages[1].Waves[0].Customers, 5)
}

func TestDarkDefaults(t *testing.T) {
	p := compileSrc(t, header+vx+"stages:\n  - name: prod\n    regions: [r]\n    dark: { validate: [x] }\n")
	tg := p.Stages[0].Waves[0].Customers[0].Targets[0]
	assert.Equal(t, "prod-dark/default/_/r", tg.ID)
	assert.Equal(t, []int{100}, tg.Steps)
}

func TestDarkChecks(t *testing.T) {
	cases := []struct {
		name, src, path, msg string
	}{
		{"no validations", header + vx + stage0 + "    dark: { mirror: [100] }\n", "stages[0].dark.validate", "needs at least one validation"},
		{"undefined validation", header + vx + stage0 + "    dark: { validate: [nope] }\n", "stages[0].dark.validate[0]", "undefined validation"},
		{"bad mirror", header + vx + stage0 + "    dark: { mirror: [50], validate: [x] }\n", "stages[0].dark.mirror", "must end at 100"},
		{"name collision", header + vx + stage0 + "    dark: { validate: [x] }\n  - name: s-dark\n    regions: [r]\n", "stages[0].dark", "collides with stage"},
		{"ramper without shadow", header + vx + stage0 + "    ramper: { plugin: team/live-only }\n    dark: { validate: [x] }\n", "stages[0].dark", "does not support shadow"},
		{"dark ramper without shadow", header + vx + stage0 + "    dark: { validate: [x], ramper: { plugin: team/live-only } }\n", "stages[0].dark", "does not support shadow"},
		{"negative bake", header + vx + stage0 + "    dark: { validate: [x], bake: -1m }\n", "stages[0].dark.bake", "negative"},
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

func TestDarkNotAllowedOnWave(t *testing.T) {
	_, diags := Compile([]byte(header+vx+stage0+"    waves:\n      - { name: w, customers: [c], dark: { validate: [x] } }\n"), loadRegistry(t), "v1")
	require.NotEmpty(t, diags)
	assert.Contains(t, diags[0].Message, "dark")
}
