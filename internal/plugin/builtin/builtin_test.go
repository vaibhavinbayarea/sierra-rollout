package builtin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/plugin"
)

func TestStubValidator(t *testing.T) {
	cases := []struct {
		name    string
		params  map[string]any
		attempt int
		want    plan.Outcome
	}{
		{"no stub", map[string]any{"path": "/h"}, 1, plan.PASS},
		{"always fail attempt 1", map[string]any{"stub": map[string]any{"outcome": "FAIL"}}, 1, plan.FAIL},
		{"always fail attempt 5", map[string]any{"stub": map[string]any{"outcome": "FAIL"}}, 5, plan.FAIL},
		{"fail before after_attempt", map[string]any{"stub": map[string]any{"outcome": "FAIL", "after_attempt": 2}}, 1, plan.FAIL},
		{"pass from after_attempt", map[string]any{"stub": map[string]any{"outcome": "FAIL", "after_attempt": 2}}, 2, plan.PASS},
		{"after_attempt as float (JSON)", map[string]any{"stub": map[string]any{"outcome": "INCONCLUSIVE", "after_attempt": 3.0}}, 2, plan.INCONCLUSIVE},
	}
	for _, name := range []string{"prometheus", "http-health"} {
		v := Register()[name].(plugin.Validator)
		for _, c := range cases {
			t.Run(name+"/"+c.name, func(t *testing.T) {
				got, err := v.Validate(context.Background(), plugin.ValidateRequest{Attempt: c.attempt, Params: c.params})
				require.NoError(t, err)
				assert.Equal(t, c.want, got.Outcome)
				assert.Equal(t, "stub: "+string(c.want), got.Summary)
			})
		}
	}
}

func TestManualApproval(t *testing.T) {
	v := Register()["manual-approval"].(plugin.Validator)
	got, err := v.Validate(context.Background(), plugin.ValidateRequest{Attempt: 1})
	require.NoError(t, err)
	assert.Equal(t, plan.AWAIT_SIGNAL, got.Outcome)
	assert.Equal(t, "awaiting approval", got.Summary)

	got, _ = v.Validate(context.Background(), plugin.ValidateRequest{Params: map[string]any{"message": "ship it?"}})
	assert.Equal(t, "ship it?", got.Summary)
}

func TestMockRamperIdempotent(t *testing.T) {
	m := NewMockRamper()
	a := plugin.Target{Agent: "x", Stage: "s", Wave: "w", Customer: "c", Region: "r1"}
	b := a
	b.Region = "r2"
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		res, err := m.SetPercent(ctx, plugin.RampRequest{Target: a, Version: "v1", Percent: 25})
		require.NoError(t, err)
		assert.Equal(t, plugin.RampResult{Percent: 25, Version: "v1"}, res)
	}
	_, err := m.SetPercent(ctx, plugin.RampRequest{Target: b, Version: "v1", Percent: 50})
	require.NoError(t, err)

	pct, ver, err := m.Current(ctx, a)
	require.NoError(t, err)
	assert.Equal(t, 25, pct)
	assert.Equal(t, "v1", ver)
	pct, _, _ = m.Current(ctx, b)
	assert.Equal(t, 50, pct)
	assert.Len(t, m.History(), 3)
}

func TestLogNotifier(t *testing.T) {
	n := Register()["log-notifier"].(plugin.Notifier)
	assert.NoError(t, n.Notify(context.Background(), plugin.Event{RolloutID: "ro/x", Kind: "failed", Message: "m"}))
}
