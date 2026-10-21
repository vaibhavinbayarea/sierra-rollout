package workflows

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"sierra-rollout/internal/plan"
)

func TestApplyProgressIgnoresStale(t *testing.T) {
	st := RolloutStatus{Targets: map[string]TargetStatus{"x": {ID: "x", State: StatePending}}}
	applyProgress(&st, TargetStatus{ID: "x", State: StateSucceeded, Seq: 5})
	applyProgress(&st, TargetStatus{ID: "x", State: StateBaking, Seq: 3})
	assert.Equal(t, StateSucceeded, st.Targets["x"].State)
	applyProgress(&st, TargetStatus{ID: "x", State: StateHeld, Seq: 6})
	assert.Equal(t, StateHeld, st.Targets["x"].State)
}

// drainWorkflow buffers progress signals while sleeping, then drains them.
func drainWorkflow(ctx workflow.Context) (RolloutStatus, error) {
	st := RolloutStatus{Targets: map[string]TargetStatus{"x": {ID: "x", State: StatePending}}}
	if err := workflow.Sleep(ctx, 2*time.Minute); err != nil {
		return st, err
	}
	drainProgress(workflow.GetSignalChannel(ctx, SignalProgress), &st)
	return st, nil
}

func TestDrainProgressAppliesPending(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(drainWorkflow)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalProgress, TargetStatus{ID: "x", State: StateBaking, Seq: 1})
		env.SignalWorkflow(SignalProgress, TargetStatus{ID: "x", State: StateSucceeded, Seq: 2})
	}, time.Minute)
	env.ExecuteWorkflow(drainWorkflow)
	require.NoError(t, env.GetWorkflowError())
	var st RolloutStatus
	require.NoError(t, env.GetWorkflowResult(&st))
	assert.Equal(t, StateSucceeded, st.Targets["x"].State)
}

func TestTargetProgressSeqIncreases(t *testing.T) {
	h := newHarness(t, pass)
	h.run(input([]plan.ValidationCall{health}, hold))
	require.NotEmpty(t, h.progress)
	for i := 1; i < len(h.progress); i++ {
		assert.Greater(t, h.progress[i].Seq, h.progress[i-1].Seq)
	}
}
