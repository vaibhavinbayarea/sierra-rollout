package workflows

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"sierra-rollout/internal/activities"
	"sierra-rollout/internal/plan"
)

type validateFn func(in activities.ValidateInput) (plan.Verdict, error)

// harness records activity calls and progress signals for one test env.
type harness struct {
	t         *testing.T
	env       *testsuite.TestWorkflowEnvironment
	mu        sync.Mutex
	ramps     []activities.RampInput
	rampTimes []time.Time
	rampErr   func(activities.RampInput) error
	validates []activities.ValidateInput
	notifies  []string
	progress  []TargetStatus
}

func pass(activities.ValidateInput) (plan.Verdict, error) {
	return plan.Verdict{Outcome: plan.PASS}, nil
}

func newHarness(t *testing.T, validate validateFn) *harness {
	h := newEnv(t, validate)
	h.env.OnSignalExternalWorkflow(mock.Anything, "ro/a", "", SignalProgress, mock.Anything).Run(func(args mock.Arguments) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.progress = append(h.progress, args.Get(4).(TargetStatus))
	}).Return(nil)
	return h
}

// newEnv registers all workflows with recording activity mocks; progress
// signals are delivered for real.
func newEnv(t *testing.T, validate validateFn) *harness {
	h := &harness{t: t}
	var s testsuite.WorkflowTestSuite
	h.env = s.NewTestWorkflowEnvironment()
	h.env.RegisterWorkflow(TargetRollout)
	h.env.RegisterWorkflow(StageRollout)
	h.env.RegisterWorkflow(GlobalRollout)
	h.env.RegisterActivity(&activities.Activities{})
	h.env.OnActivity("Ramp", mock.Anything, mock.Anything).Return(func(_ context.Context, in activities.RampInput) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.rampErr != nil {
			if err := h.rampErr(in); err != nil {
				return err
			}
		}
		h.ramps = append(h.ramps, in)
		h.rampTimes = append(h.rampTimes, h.env.Now())
		return nil
	})
	h.env.OnActivity("Validate", mock.Anything, mock.Anything).Return(func(_ context.Context, in activities.ValidateInput) (plan.Verdict, error) {
		h.mu.Lock()
		h.validates = append(h.validates, in)
		h.mu.Unlock()
		if in.Validation.Plugin == "manual-approval" {
			return plan.Verdict{Outcome: plan.AWAIT_SIGNAL}, nil
		}
		return validate(in)
	})
	h.env.OnActivity("Notify", mock.Anything, mock.Anything).Return(func(_ context.Context, in activities.NotifyInput) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.notifies = append(h.notifies, in.Event.Kind)
		return nil
	})
	return h
}

func (h *harness) signalAt(d time.Duration, name string, arg any) {
	h.env.RegisterDelayedCallback(func() { h.env.SignalWorkflow(name, arg) }, d)
}

func (h *harness) run(in TargetInput) TargetResult {
	h.env.ExecuteWorkflow(TargetRollout, in)
	require.True(h.t, h.env.IsWorkflowCompleted())
	require.NoError(h.t, h.env.GetWorkflowError())
	var res TargetResult
	require.NoError(h.t, h.env.GetWorkflowResult(&res))
	return res
}

func (h *harness) rampPercents() []int {
	var out []int
	for _, r := range h.ramps {
		out = append(out, r.Percent)
	}
	return out
}

func (h *harness) validatesOf(name string) []activities.ValidateInput {
	var out []activities.ValidateInput
	for _, v := range h.validates {
		if v.Validation.Name == name {
			out = append(out, v)
		}
	}
	return out
}

func (h *harness) lastState() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.progress) == 0 {
		return ""
	}
	return h.progress[len(h.progress)-1].State
}

var (
	health  = plan.ValidationCall{Name: "health", Plugin: "http-health", Severity: "critical", When: "every_step"}
	signoff = plan.ValidationCall{Name: "signoff", Plugin: "manual-approval", Severity: "critical", When: "final_step"}
	csat    = plan.ValidationCall{Name: "csat", Plugin: "eval-team/csat", Severity: "warning", When: "every_step"}

	notifyLog = plan.FailureAction{Kind: "notify", Notifier: "log-notifier"}
	pause24h  = plan.FailureAction{Kind: "pause", Timeout: 24 * time.Hour, OnTimeout: "hold"}
	hold      = plan.FailureAction{Kind: "hold"}
)

// input builds a two-step (50%, 100%) target with a 5m bake. Step 1's
// validations run at +5m, step 2's at +10m.
func input(vs []plan.ValidationCall, onFailure ...plan.FailureAction) TargetInput {
	return TargetInput{
		GlobalID: "ro/a", Agent: "a", Version: "v1",
		Target: plan.TargetPlan{
			ID: "prod/w/c/r", Stage: "prod", Wave: "w", Customer: "c", Region: "r",
			Ramper: plan.PluginCall{Plugin: "mock-ramper"}, Steps: []int{50, 100}, Bake: 5 * time.Minute,
			Validations: vs,
			Decision: plan.DecisionRule{Critical: "all_pass", Warning: "notify", WarningNotifier: "log-notifier",
				Inconclusive: plan.InconclusiveRule{Retry: 3, Backoff: time.Minute, Then: "fail"}},
			OnFailure: onFailure,
		},
		Plugins: map[string]plan.PluginRef{
			"mock-ramper":     {Name: "mock-ramper", Kind: "ramper", Timeout: 5 * time.Minute},
			"http-health":     {Name: "http-health", Kind: "validator", Timeout: 2 * time.Minute},
			"manual-approval": {Name: "manual-approval", Kind: "validator", Timeout: 2 * time.Minute},
			"eval-team/csat":  {Name: "eval-team/csat", Kind: "validator", Timeout: 2 * time.Minute},
			"log-notifier":    {Name: "log-notifier", Kind: "notifier", Timeout: 30 * time.Second},
		},
	}
}

// failFirstAttempt fails the named step's first attempt and passes otherwise.
func failFirstAttempt(steps ...int) validateFn {
	return func(in activities.ValidateInput) (plan.Verdict, error) {
		for _, s := range steps {
			if in.Step == s && in.Attempt == 1 {
				return plan.Verdict{Outcome: plan.FAIL}, nil
			}
		}
		return plan.Verdict{Outcome: plan.PASS}, nil
	}
}

func TestTargetHappyPath(t *testing.T) {
	h := newHarness(t, pass)
	h.signalAt(11*time.Minute, SignalApprove, OperatorSignal{Validation: "signoff"})
	res := h.run(input([]plan.ValidationCall{health, signoff}, pause24h))
	assert.Equal(t, Succeeded, res.Status)
	assert.Equal(t, []int{50, 100}, h.rampPercents())
	assert.Len(t, h.validatesOf("signoff"), 1)
	assert.Equal(t, 1, h.validatesOf("signoff")[0].Step)
	assert.Len(t, h.validatesOf("health"), 2)
}

func TestTargetFailPauseResume(t *testing.T) {
	h := newHarness(t, failFirstAttempt(0))
	h.signalAt(time.Hour, SignalResume, OperatorSignal{})
	res := h.run(input([]plan.ValidationCall{health}, notifyLog, pause24h))
	assert.Equal(t, Succeeded, res.Status)
	assert.Equal(t, []string{"failed", "paused"}, h.notifies)
	assert.Equal(t, []int{50, 100}, h.rampPercents())
}

func TestTargetPauseRetry(t *testing.T) {
	h := newHarness(t, failFirstAttempt(0))
	h.signalAt(time.Hour, SignalRetry, OperatorSignal{})
	res := h.run(input([]plan.ValidationCall{health}, pause24h))
	assert.Equal(t, Succeeded, res.Status)
	vs := h.validatesOf("health")
	require.GreaterOrEqual(t, len(vs), 2)
	assert.Equal(t, 0, vs[1].Step)
	assert.Equal(t, 2, vs[1].Attempt)
}

func TestTargetPauseAbortSignal(t *testing.T) {
	h := newHarness(t, failFirstAttempt(0))
	h.signalAt(time.Hour, SignalAbort, OperatorSignal{})
	res := h.run(input([]plan.ValidationCall{health}, pause24h))
	assert.Equal(t, Aborted, res.Status)
	assert.Equal(t, []int{50}, h.rampPercents())
}

func TestTargetPauseTimeoutHold(t *testing.T) {
	h := newHarness(t, failFirstAttempt(0))
	res := h.run(input([]plan.ValidationCall{health}, pause24h))
	assert.Equal(t, Held, res.Status)
	assert.Equal(t, StateHeld, h.lastState())
}

func TestTargetInconclusiveRetryThenFail(t *testing.T) {
	h := newHarness(t, func(activities.ValidateInput) (plan.Verdict, error) {
		return plan.Verdict{Outcome: plan.INCONCLUSIVE}, nil
	})
	res := h.run(input([]plan.ValidationCall{health}, hold))
	assert.Equal(t, Held, res.Status)
	assert.Len(t, h.validatesOf("health"), 4)
}

func TestTargetInconclusiveRecovers(t *testing.T) {
	h := newHarness(t, func(in activities.ValidateInput) (plan.Verdict, error) {
		if in.Attempt == 1 {
			return plan.Verdict{Outcome: plan.INCONCLUSIVE}, nil
		}
		return plan.Verdict{Outcome: plan.PASS}, nil
	})
	res := h.run(input([]plan.ValidationCall{health}, hold))
	assert.Equal(t, Succeeded, res.Status)
}

func TestTargetValidateErrorIsInconclusive(t *testing.T) {
	h := newHarness(t, func(in activities.ValidateInput) (plan.Verdict, error) {
		if in.Attempt == 1 {
			return plan.Verdict{}, errors.New("plugin down")
		}
		return plan.Verdict{Outcome: plan.PASS}, nil
	})
	res := h.run(input([]plan.ValidationCall{health}, hold))
	assert.Equal(t, Succeeded, res.Status)
	vs := h.validatesOf("health")
	assert.Equal(t, 2, vs[len(vs)-1].Attempt)
}

func TestTargetPluginMissingFails(t *testing.T) {
	h := newHarness(t, func(activities.ValidateInput) (plan.Verdict, error) {
		return plan.Verdict{}, temporal.NewNonRetryableApplicationError("missing", activities.ErrTypePluginMissing, nil)
	})
	res := h.run(input([]plan.ValidationCall{health}, hold))
	assert.Equal(t, Held, res.Status)
	assert.Len(t, h.validatesOf("health"), 1)
	assert.Contains(t, res.Message, "missing")
}

func TestTargetRejectFails(t *testing.T) {
	h := newHarness(t, pass)
	h.signalAt(11*time.Minute, SignalReject, OperatorSignal{Validation: "signoff"})
	res := h.run(input([]plan.ValidationCall{health, signoff}, hold))
	assert.Equal(t, Held, res.Status)
	assert.Contains(t, res.Message, "signoff")
}

func TestTargetWarningNotifies(t *testing.T) {
	h := newHarness(t, func(in activities.ValidateInput) (plan.Verdict, error) {
		if in.Validation.Name == "csat" {
			return plan.Verdict{Outcome: plan.FAIL}, nil
		}
		return plan.Verdict{Outcome: plan.PASS}, nil
	})
	res := h.run(input([]plan.ValidationCall{health, csat}, hold))
	assert.Equal(t, Succeeded, res.Status)
	assert.Equal(t, []string{"warning", "warning"}, h.notifies)
}

func TestTargetStopAfterStep(t *testing.T) {
	h := newHarness(t, pass)
	h.signalAt(time.Minute, SignalStop, nil)
	res := h.run(input([]plan.ValidationCall{health}, hold))
	assert.Equal(t, Held, res.Status)
	assert.Equal(t, []int{50}, h.rampPercents())
	assert.Contains(t, res.Message, "sibling")
}

func TestTargetCancelled(t *testing.T) {
	h := newHarness(t, pass)
	h.env.RegisterDelayedCallback(h.env.CancelWorkflow, time.Minute)
	res := h.run(input([]plan.ValidationCall{health}, notifyLog, pause24h))
	assert.Equal(t, Aborted, res.Status)
	assert.Equal(t, []string{"aborted"}, h.notifies)
	assert.Equal(t, StateAborted, h.lastState())
}

func TestEarlyApproveIsIgnored(t *testing.T) {
	h := newHarness(t, pass)
	h.signalAt(time.Minute, SignalApprove, OperatorSignal{Validation: "signoff"})
	var stateAt48h string
	h.env.RegisterDelayedCallback(func() {
		stateAt48h = h.lastState()
		h.env.CancelWorkflow()
	}, 48*time.Hour)
	res := h.run(input([]plan.ValidationCall{health, signoff}, hold))
	assert.Equal(t, StateAwaitingApproval, stateAt48h)
	assert.Equal(t, Aborted, res.Status)
}

func TestDuplicateResumeIgnored(t *testing.T) {
	h := newHarness(t, failFirstAttempt(0, 1))
	h.signalAt(time.Hour, SignalResume, OperatorSignal{})
	h.signalAt(time.Hour, SignalResume, OperatorSignal{})
	res := h.run(input([]plan.ValidationCall{health}, pause24h))
	// Had the second resume been buffered, step 2's pause would resume
	// immediately and the target would succeed.
	assert.Equal(t, Held, res.Status)
	assert.Equal(t, []int{50, 100}, h.rampPercents())
}

func TestProgressSignals(t *testing.T) {
	h := newHarness(t, pass)
	h.signalAt(11*time.Minute, SignalApprove, OperatorSignal{})
	h.run(input([]plan.ValidationCall{health, signoff}, hold))
	var states []string
	for _, p := range h.progress {
		if len(states) == 0 || states[len(states)-1] != p.State {
			states = append(states, p.State)
		}
	}
	assert.Subset(t, states, []string{StateRamping, StateBaking, StateValidating, StateAwaitingApproval, StateSucceeded})
	assert.Equal(t, StateSucceeded, states[len(states)-1])
	last := h.progress[len(h.progress)-1]
	assert.Equal(t, "prod/w/c/r", last.ID)
	assert.Equal(t, 100, last.Percent)
	assert.Equal(t, plan.PASS, last.Verdicts["signoff"].Outcome)
}
