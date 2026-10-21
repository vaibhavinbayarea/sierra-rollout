package workflows

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	"sierra-rollout/internal/activities"
	"sierra-rollout/internal/compiler"
	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/registry"
)

func darkInput(onFailure ...plan.FailureAction) TargetInput {
	in := input([]plan.ValidationCall{health}, onFailure...)
	in.Target.Mode = "dark"
	in.Target.ID, in.Target.Stage = "prod-dark/w/c/r", "prod-dark"
	return in
}

type ramp struct {
	pct  int
	mode string
}

func (h *harness) rampsWithMode() []ramp {
	var out []ramp
	for _, r := range h.ramps {
		out = append(out, ramp{r.Percent, r.Mode})
	}
	return out
}

func TestDarkTargetRampsShadowAndTearsDown(t *testing.T) {
	h := newHarness(t, pass)
	res := h.run(darkInput(hold))
	assert.Equal(t, Succeeded, res.Status)
	assert.Equal(t, []ramp{{50, "shadow"}, {100, "shadow"}, {0, "shadow"}}, h.rampsWithMode())
	last := h.progress[len(h.progress)-1]
	assert.Equal(t, "dark", last.Mode)
	assert.Equal(t, StateSucceeded, last.State)
	assert.Equal(t, 0, last.Percent, "mirroring torn down")
}

func TestDarkTargetTearsDownOnHold(t *testing.T) {
	h := newHarness(t, failFirstAttempt(0))
	res := h.run(darkInput(hold))
	assert.Equal(t, Held, res.Status)
	assert.Equal(t, []ramp{{50, "shadow"}, {0, "shadow"}}, h.rampsWithMode())
}

func TestDarkTargetTearsDownWhenStopped(t *testing.T) {
	h := newHarness(t, pass)
	h.signalAt(time.Minute, SignalStop, nil)
	res := h.run(darkInput(hold))
	assert.Equal(t, Held, res.Status)
	assert.Equal(t, []ramp{{50, "shadow"}, {0, "shadow"}}, h.rampsWithMode())
}

func TestDarkTargetTearsDownOnCancel(t *testing.T) {
	h := newHarness(t, pass)
	h.env.RegisterDelayedCallback(h.env.CancelWorkflow, time.Minute)
	res := h.run(darkInput(hold))
	assert.Equal(t, Aborted, res.Status)
	assert.Equal(t, []ramp{{50, "shadow"}, {0, "shadow"}}, h.rampsWithMode())
}

func TestDarkTeardownFailureKeepsResult(t *testing.T) {
	h := newHarness(t, pass)
	h.rampErr = func(in activities.RampInput) error {
		if in.Percent == 0 {
			return temporal.NewNonRetryableApplicationError("mirror api down", "RampError", nil)
		}
		return nil
	}
	res := h.run(darkInput(hold))
	assert.Equal(t, Succeeded, res.Status)
	assert.Contains(t, res.Message, "mirror teardown failed")
}

func TestLiveTargetHasNoMode(t *testing.T) {
	h := newHarness(t, pass)
	h.run(input([]plan.ValidationCall{health}, hold))
	for _, r := range h.ramps {
		assert.Empty(t, r.Mode)
	}
	assert.Empty(t, h.progress[len(h.progress)-1].Mode)
}

func darkPlan(t *testing.T) plan.Plan {
	t.Helper()
	src, err := os.ReadFile("../compiler/testdata/dark.yaml")
	require.NoError(t, err)
	reg, err := registry.Load("../compiler/testdata/plugins.yaml")
	require.NoError(t, err)
	p, diags := compiler.Compile(src, reg, "v1")
	require.Empty(t, diags)
	return *p
}

func TestGlobalDarkGatesProd(t *testing.T) {
	h := newEnv(t, pass)
	st := h.runGlobal(darkPlan(t))
	assert.Equal(t, Succeeded, st.State)
	lastShadow, firstLiveProd := -1, -1
	for i, r := range h.ramps {
		if r.Mode == "shadow" {
			lastShadow = i
		}
		if r.Target.Stage == "prod" && firstLiveProd < 0 {
			firstLiveProd = i
		}
	}
	require.GreaterOrEqual(t, firstLiveProd, 0)
	assert.Less(t, lastShadow, firstLiveProd, "dark phase completes before any live prod traffic")
	assert.Equal(t, StateSucceeded, st.Targets["prod-dark/broad/initech/eu-west-1"].State)
}

func TestGlobalFailingDarkSkipsProd(t *testing.T) {
	h := newEnv(t, failStage("prod-dark"))
	st := h.runGlobal(darkPlan(t))
	assert.Equal(t, Held, st.State)
	for _, id := range st.Order {
		if st.Targets[id].Stage == "prod" {
			assert.Equal(t, StateSkipped, st.Targets[id].State, id)
		}
	}
	for _, r := range h.ramps {
		assert.NotEqual(t, "prod", r.Target.Stage, "no live prod traffic")
	}
}
