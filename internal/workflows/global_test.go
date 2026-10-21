package workflows

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	"sierra-rollout/internal/activities"
	"sierra-rollout/internal/compiler"
	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/registry"
)

// fullPlan compiles the spec example without manual approval, with
// optional extra YAML edits.
func fullPlan(t *testing.T, edits ...string) plan.Plan {
	t.Helper()
	src, err := os.ReadFile("../compiler/testdata/full.yaml")
	require.NoError(t, err)
	s := strings.Replace(string(src), ", signoff]", "]", 1)
	for i := 0; i+1 < len(edits); i += 2 {
		s = strings.Replace(s, edits[i], edits[i+1], 1)
	}
	reg, err := registry.Load("../compiler/testdata/plugins.yaml")
	require.NoError(t, err)
	p, diags := compiler.Compile([]byte(s), reg, "v1")
	require.Empty(t, diags)
	return *p
}

func (h *harness) runGlobal(p plan.Plan) RolloutStatus {
	h.env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: p.ID})
	h.env.ExecuteWorkflow(GlobalRollout, p)
	require.True(h.t, h.env.IsWorkflowCompleted())
	require.NoError(h.t, h.env.GetWorkflowError())
	var st RolloutStatus
	require.NoError(h.t, h.env.GetWorkflowResult(&st))
	return st
}

func failStage(stage string) validateFn {
	return func(in activities.ValidateInput) (plan.Verdict, error) {
		if in.Target.Stage == stage {
			return plan.Verdict{Outcome: plan.FAIL}, nil
		}
		return plan.Verdict{Outcome: plan.PASS}, nil
	}
}

func TestGlobalHappyPath(t *testing.T) {
	h := newEnv(t, pass)
	st := h.runGlobal(fullPlan(t))
	assert.Equal(t, Succeeded, st.State)
	require.Len(t, st.Targets, 8)
	require.Len(t, st.Order, 8)
	for _, id := range st.Order {
		assert.Equal(t, StateSucceeded, st.Targets[id].State, id)
	}
}

func TestGlobalStopsAtHeldStage(t *testing.T) {
	h := newEnv(t, failStage("pre-prod"))
	st := h.runGlobal(fullPlan(t))
	assert.Equal(t, Held, st.State)
	assert.Equal(t, StateSucceeded, st.Targets["staging/default/_/us-east-1"].State)
	assert.Equal(t, StateHeld, st.Targets["pre-prod/default/_/us-east-1"].State)
	assert.Equal(t, StateSkipped, st.Targets["pre-prod/default/_/eu-west-1"].State)
	assert.Equal(t, StateSkipped, st.Targets["prod/canary/acme/us-east-1"].State)
	assert.Contains(t, st.Targets["prod/canary/acme/us-east-1"].Message, "not started")
}

func TestGlobalAbortWhilePaused(t *testing.T) {
	h := newEnv(t, failStage("pre-prod"))
	start := h.env.Now()
	var stateBeforeAbort string
	h.env.RegisterDelayedCallback(func() {
		v, err := h.env.QueryWorkflow(QueryStatus)
		require.NoError(t, err)
		var st RolloutStatus
		require.NoError(t, v.Get(&st))
		stateBeforeAbort = st.Targets["pre-prod/default/_/us-east-1"].State
		h.env.SignalWorkflow(SignalAbort, nil)
	}, 2*time.Hour)
	st := h.runGlobal(fullPlan(t))
	assert.Equal(t, StatePaused, stateBeforeAbort)
	assert.Equal(t, Aborted, st.State)
	assert.Equal(t, StateAborted, st.Targets["pre-prod/default/_/us-east-1"].State)
	assert.Equal(t, StateSkipped, st.Targets["prod/broad/initech/eu-west-1"].State)
	assert.Less(t, h.env.Now().Sub(start), 3*time.Hour)
}

func TestGlobalStatusQueryMidRun(t *testing.T) {
	h := newEnv(t, pass)
	var mid RolloutStatus
	h.env.RegisterDelayedCallback(func() {
		v, err := h.env.QueryWorkflow(QueryStatus)
		require.NoError(t, err)
		require.NoError(t, v.Get(&mid))
	}, time.Minute)
	h.runGlobal(fullPlan(t))
	assert.Equal(t, "running", mid.State)
	assert.Equal(t, StateBaking, mid.Targets["staging/default/_/us-east-1"].State)
	assert.Equal(t, StatePending, mid.Targets["prod/broad/globex/eu-west-1"].State)
	assert.Equal(t, "ro/support-agent", mid.ID)
}
