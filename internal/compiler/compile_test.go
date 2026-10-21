package compiler

import (
	"encoding/json"
	"flag"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/registry"
)

var update = flag.Bool("update", false, "rewrite golden files")

func loadRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	r, err := registry.Load("testdata/plugins.yaml")
	require.NoError(t, err)
	return r
}

func compileFile(t *testing.T, name string) *plan.Plan {
	t.Helper()
	src, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	p, diags := Compile(src, loadRegistry(t), "v1.4.2")
	require.Empty(t, diags)
	require.NotNil(t, p)
	return p
}

func compileSrc(t *testing.T, src string) *plan.Plan {
	t.Helper()
	p, diags := Compile([]byte(src), loadRegistry(t), "v1")
	require.Empty(t, diags)
	return p
}

func TestCompileGolden(t *testing.T) {
	for _, name := range []string{"full", "minimal", "staging-only"} {
		t.Run(name, func(t *testing.T) {
			got, err := json.MarshalIndent(compileFile(t, name+".yaml"), "", "  ")
			require.NoError(t, err)
			golden := "testdata/" + name + ".plan.json"
			if *update {
				require.NoError(t, os.WriteFile(golden, append(got, '\n'), 0o644))
			}
			want, err := os.ReadFile(golden)
			require.NoError(t, err)
			assert.JSONEq(t, string(want), string(got))
		})
	}
}

func TestCompileFullShape(t *testing.T) {
	p := compileFile(t, "full.yaml")
	var ids []string
	for _, tg := range p.Targets() {
		ids = append(ids, tg.ID)
	}
	assert.Equal(t, []string{
		"staging/default/_/us-east-1",
		"pre-prod/default/_/us-east-1",
		"pre-prod/default/_/eu-west-1",
		"prod/canary/acme/us-east-1",
		"prod/broad/globex/us-east-1",
		"prod/broad/globex/eu-west-1",
		"prod/broad/initech/us-east-1",
		"prod/broad/initech/eu-west-1",
	}, ids)
	broad := p.Stages[2].Waves[1]
	assert.Equal(t, 2, broad.MaxParallel)
	assert.Equal(t, []int{25, 100}, broad.Customers[0].Targets[0].Steps)
	assert.Equal(t, "ro/support-agent", p.ID)
	assert.Contains(t, p.Plugins, "eval-team/csat")
	assert.Equal(t, 2*time.Minute, p.Plugins["eval-team/csat"].Timeout)
	assert.Equal(t, 5*time.Minute, p.Plugins["mock-ramper"].Timeout)
	assert.Equal(t, 30*time.Second, p.Plugins["log-notifier"].Timeout)
	assert.NotContains(t, p.Plugins, "nonexistent")
	assert.Len(t, p.PolicyHash, 64)
}

func TestCompileDeterministic(t *testing.T) {
	a, _ := json.Marshal(compileFile(t, "full.yaml"))
	b, _ := json.Marshal(compileFile(t, "full.yaml"))
	assert.Equal(t, string(a), string(b))
}

func TestOverridePrecedence(t *testing.T) {
	p := compileSrc(t, `
apiVersion: rollout/v1
agent: a
ramper: { plugin: mock-ramper }
defaults: { bake: 5m }
stages:
  - name: s1
    regions: [r1]
    bake: 1m
    waves:
      - { name: w1, customers: [c1], bake: 10s }
      - { name: w2, customers: [c2] }
  - name: s2
    regions: [r1]
`)
	ts := p.Targets()
	assert.Equal(t, 10*time.Second, ts[0].Bake)
	assert.Equal(t, time.Minute, ts[1].Bake)
	assert.Equal(t, 5*time.Minute, ts[2].Bake)

	none := compileSrc(t, "apiVersion: rollout/v1\nagent: a\nramper: { plugin: mock-ramper }\nstages: [{ name: s, regions: [r] }]\n")
	assert.Equal(t, time.Duration(0), none.Targets()[0].Bake)
}

func TestBuiltinDefaults(t *testing.T) {
	tg := compileFile(t, "minimal.yaml").Targets()[0]
	assert.Equal(t, []int{100}, tg.Steps)
	assert.Equal(t, []plan.FailureAction{{Kind: "pause", Timeout: 24 * time.Hour, OnTimeout: "hold"}}, tg.OnFailure)
	assert.Equal(t, plan.InconclusiveRule{Retry: 3, Backoff: time.Minute, Then: "fail"}, tg.Decision.Inconclusive)
	assert.Equal(t, "all_pass", tg.Decision.Critical)
	assert.Equal(t, "notify", tg.Decision.Warning)
	assert.Equal(t, "log-notifier", tg.Decision.WarningNotifier)
	assert.Equal(t, "_", tg.Customer)
	assert.Equal(t, "default", tg.Wave)
}

func TestValidationDefaultsAndWarningNotifier(t *testing.T) {
	tg := compileFile(t, "staging-only.yaml").Targets()[0]
	require.Len(t, tg.Validations, 1)
	assert.Equal(t, plan.ValidationCall{Name: "health", Plugin: "http-health", Params: map[string]any{"path": "/healthz"},
		Severity: "critical", When: "every_step"}, tg.Validations[0])
	assert.Equal(t, "ignore", tg.Decision.Warning)
	assert.Equal(t, "log-notifier", tg.Decision.WarningNotifier)
	assert.Equal(t, []plan.FailureAction{{Kind: "notify", Notifier: "log-notifier"}, {Kind: "hold"}}, tg.OnFailure)
}

func TestIDs(t *testing.T) {
	assert.Equal(t, "ro/a", plan.GlobalID("a", "v2", false))
	assert.Equal(t, "ro/a/v2", plan.GlobalID("a", "v2", true))
	assert.Equal(t, "ro/a/stage/prod", plan.StageID("a", "prod"))
	assert.Equal(t, "ro/a/prod/broad/acme/us-east-1",
		plan.TargetWorkflowID("ro/a", plan.TargetPlan{ID: "prod/broad/acme/us-east-1"}))
	assert.True(t, strings.HasPrefix(compileFile(t, "staging-only.yaml").ID, "ro/solo/v1.4.2"))
}
