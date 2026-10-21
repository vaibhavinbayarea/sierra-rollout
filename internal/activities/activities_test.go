package activities

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/plugin"
	"sierra-rollout/internal/plugin/builtin"
	"sierra-rollout/internal/registry"
)

func setup(t *testing.T) (*testsuite.TestActivityEnvironment, *builtin.MockRamper) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugins.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`plugins:
  - { name: prometheus,   kind: validator, builtin: true }
  - { name: mock-ramper,  kind: ramper,    builtin: true }
  - { name: log-notifier, kind: notifier,  builtin: true }
`), 0o644))
	reg, err := registry.Load(path)
	require.NoError(t, err)
	builtins := builtin.Register()
	set, err := plugin.NewSet(reg, builtins, nil)
	require.NoError(t, err)

	var s testsuite.WorkflowTestSuite
	env := s.NewTestActivityEnvironment()
	env.RegisterActivity(&Activities{Plugins: set})
	return env, builtins["mock-ramper"].(*builtin.MockRamper)
}

func TestRampRecordsCall(t *testing.T) {
	env, mock := setup(t)
	_, err := env.ExecuteActivity("Ramp", RampInput{
		Ramper: plan.PluginCall{Plugin: "mock-ramper"}, Target: plugin.Target{Agent: "a", Region: "r"}, Version: "v1", Percent: 25,
	})
	require.NoError(t, err)
	h := mock.History()
	require.Len(t, h, 1)
	assert.Equal(t, 25, h[0].Percent)
	assert.Equal(t, "r", h[0].Target.Region)
}

func TestValidateReturnsVerdict(t *testing.T) {
	env, _ := setup(t)
	val, err := env.ExecuteActivity("Validate", ValidateInput{
		Validation: plan.ValidationCall{Name: "error-rate", Plugin: "prometheus",
			Params: map[string]any{"query": "q", "stub": map[string]any{"outcome": "FAIL"}}},
		Attempt: 1,
	})
	require.NoError(t, err)
	var v plan.Verdict
	require.NoError(t, val.Get(&v))
	assert.Equal(t, plan.FAIL, v.Outcome)
}

func TestValidateMissingPluginIsNonRetryable(t *testing.T) {
	env, _ := setup(t)
	_, err := env.ExecuteActivity("Validate", ValidateInput{Validation: plan.ValidationCall{Name: "x", Plugin: "nope"}})
	var appErr *temporal.ApplicationError
	require.True(t, errors.As(err, &appErr), "got %v", err)
	assert.Equal(t, ErrTypePluginMissing, appErr.Type())
	assert.True(t, appErr.NonRetryable())
}

func TestNotify(t *testing.T) {
	env, _ := setup(t)
	_, err := env.ExecuteActivity("Notify", NotifyInput{Notifier: "log-notifier", Event: plugin.Event{Kind: "failed"}})
	assert.NoError(t, err)
}
