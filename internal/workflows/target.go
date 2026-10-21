package workflows

import (
	"errors"
	"fmt"
	"maps"
	"sort"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"sierra-rollout/internal/activities"
	"sierra-rollout/internal/decision"
	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/plugin"
)

type TargetInput struct {
	GlobalID string
	Agent    string
	Version  string
	Target   plan.TargetPlan
	Plugins  map[string]plan.PluginRef
}

type TargetResult struct {
	Status  string
	Message string
}

// errAbort is returned internally when an operator aborts a paused target.
var errAbort = errors.New("aborted by operator")

// failure policy outcomes
type outcome int

const (
	outPassed outcome = iota // the retried operation succeeded
	outResume                // operator resumed past the failure
	outHeld
)

type runner struct {
	ctx      workflow.Context
	in       TargetInput
	t        plan.TargetPlan
	status   TargetStatus
	attempts map[string]int // per validation, reset each step

	// set by the signal loop, consumed by waiters
	stop      bool
	pauseCmd  string
	awaiting  map[string]bool
	approvals map[string]plan.Outcome
}

// TargetRollout ramps one target through its steps, validating after each.
func TargetRollout(ctx workflow.Context, in TargetInput) (TargetResult, error) {
	t := in.Target
	r := &runner{ctx: ctx, in: in, t: t, status: TargetStatus{
		ID: t.ID, Stage: t.Stage, Wave: t.Wave, Customer: t.Customer, Region: t.Region,
		State: StatePending, Steps: len(t.Steps), Verdicts: map[string]plan.Verdict{},
	}}
	r.signalLoop()
	res, err := r.run()
	if err != nil {
		if errors.Is(err, errAbort) || r.canceled(err) {
			return r.abort(), nil
		}
		return TargetResult{}, err
	}
	return res, nil
}

func (r *runner) run() (TargetResult, error) {
	for i, pct := range r.t.Steps {
		r.attempts = map[string]int{}
		r.status.Step = i + 1

		for {
			err := r.ramp(pct)
			if err == nil {
				break
			}
			if r.canceled(err) {
				return TargetResult{}, err
			}
			out, msg, err := r.failurePolicy(fmt.Sprintf("ramp to %d%% failed: %v", pct, err), func() (bool, error) {
				err := r.ramp(pct)
				if r.canceled(err) {
					return false, err
				}
				return err == nil, nil
			})
			if err != nil {
				return TargetResult{}, err
			}
			if out == outHeld {
				return r.held(msg), nil
			}
			if out == outPassed {
				break
			}
			// outResume: try the ramp again
		}

		r.setState(StateBaking, "")
		if err := workflow.Sleep(r.ctx, r.t.Bake); err != nil {
			return TargetResult{}, err
		}

		res, err := r.validateStep(i, pct)
		if err != nil {
			return TargetResult{}, err
		}
		if res.Action == decision.Fail {
			out, msg, err := r.failurePolicy(res.Reason, func() (bool, error) {
				res, err := r.validateStep(i, pct)
				return err == nil && res.Action != decision.Fail, err
			})
			if err != nil {
				return TargetResult{}, err
			}
			if out == outHeld {
				return r.held(msg), nil
			}
		}

		if r.stop && i < len(r.t.Steps)-1 {
			return r.end(Held, StateHeld, "stopped: sibling target failed"), nil
		}
	}
	return r.end(Succeeded, StateSucceeded, ""), nil
}

func (r *runner) ramp(pct int) error {
	r.setState(StateRamping, "")
	ctx := r.activityCtx(r.t.Ramper.Plugin, 5*time.Minute, 5)
	err := workflow.ExecuteActivity(ctx, "Ramp", activities.RampInput{
		Ramper: r.t.Ramper, Target: r.target(), Version: r.in.Version, Percent: pct,
	}).Get(ctx, nil)
	if err == nil {
		r.status.Percent = pct
	}
	return err
}

// validateStep runs the step's validations, retrying inconclusive ones and
// waiting for approvals, and returns Continue or Fail.
func (r *runner) validateStep(step, pct int) (decision.Result, error) {
	var applicable []plan.ValidationCall
	for _, v := range r.t.Validations {
		if v.When != "final_step" || step == len(r.t.Steps)-1 {
			applicable = append(applicable, v)
		}
	}
	verdicts := map[string]decision.NamedVerdict{}
	toRun := applicable
	for inconclusiveAttempt := 0; ; inconclusiveAttempt++ {
		r.setState(StateValidating, "")
		futures := make([]workflow.Future, len(toRun))
		for k, v := range toRun {
			r.attempts[v.Name]++
			ctx := r.activityCtx(v.Plugin, 2*time.Minute, 3)
			futures[k] = workflow.ExecuteActivity(ctx, "Validate", activities.ValidateInput{
				Validation: v, Target: r.target(), Version: r.in.Version,
				Step: step, Percent: pct, Attempt: r.attempts[v.Name],
			})
		}
		var awaiting []string
		for k, v := range toRun {
			var verdict plan.Verdict
			if err := futures[k].Get(r.ctx, &verdict); err != nil {
				if r.canceled(err) {
					return decision.Result{}, err
				}
				var appErr *temporal.ApplicationError
				if errors.As(err, &appErr) && appErr.Type() == activities.ErrTypePluginMissing {
					return decision.Result{Action: decision.Fail, Reason: fmt.Sprintf("validation %s: %s", v.Name, appErr.Message())}, nil
				}
				verdict = plan.Verdict{Outcome: plan.INCONCLUSIVE, Summary: err.Error()}
			}
			if verdict.Outcome == plan.AWAIT_SIGNAL {
				awaiting = append(awaiting, v.Name)
			}
			verdicts[v.Name] = decision.NamedVerdict{Name: v.Name, Severity: v.Severity, Verdict: verdict}
			r.status.Verdicts[v.Name] = verdict
		}
		if len(awaiting) > 0 {
			if err := r.awaitApprovals(awaiting, verdicts); err != nil {
				return decision.Result{}, err
			}
		}

		ordered := make([]decision.NamedVerdict, 0, len(applicable))
		for _, v := range applicable {
			ordered = append(ordered, verdicts[v.Name])
		}
		res := decision.Decide(ordered, r.t.Decision, inconclusiveAttempt)
		if res.Action != decision.RetryInconclusive {
			for _, name := range res.NotifyWarnings {
				r.notify(r.t.Decision.WarningNotifier, "warning", fmt.Sprintf("warning validation %s failed at %d%%", name, pct))
			}
			return res, nil
		}
		r.setState(StateRetrying, res.Reason)
		if err := workflow.Sleep(r.ctx, res.Backoff); err != nil {
			return decision.Result{}, err
		}
		toRun = toRun[:0:0]
		for _, v := range applicable {
			for _, name := range res.Retry {
				if v.Name == name {
					toRun = append(toRun, v)
				}
			}
		}
	}
}

// awaitApprovals waits until every awaiting validation is approved or any
// is rejected, and records the operator's verdicts.
func (r *runner) awaitApprovals(names []string, verdicts map[string]decision.NamedVerdict) error {
	r.awaiting, r.approvals = map[string]bool{}, map[string]plan.Outcome{}
	for _, n := range names {
		r.awaiting[n] = true
	}
	r.status.AwaitingApproval = names
	r.setState(StateAwaitingApproval, "")
	err := workflow.Await(r.ctx, func() bool {
		for _, o := range r.approvals {
			if o == plan.FAIL {
				return true
			}
		}
		return len(r.approvals) == len(r.awaiting)
	})
	for name, o := range r.approvals {
		summary := "approved by operator"
		if o == plan.FAIL {
			summary = "rejected by operator"
		}
		nv := verdicts[name]
		nv.Verdict = plan.Verdict{Outcome: o, Summary: summary}
		verdicts[name] = nv
		r.status.Verdicts[name] = nv.Verdict
	}
	r.awaiting, r.approvals, r.status.AwaitingApproval = nil, nil, nil
	return err
}

// failurePolicy runs the target's on_failure actions in order. retry
// re-attempts the failed operation and reports whether it succeeded.
func (r *runner) failurePolicy(reason string, retry func() (bool, error)) (outcome, string, error) {
	r.status.Message = reason
	for _, a := range r.t.OnFailure {
		switch a.Kind {
		case "notify":
			r.notify(a.Notifier, "failed", reason)
		case "retry":
			for n := 0; n < a.Max; n++ {
				r.setState(StateRetrying, reason)
				if err := workflow.Sleep(r.ctx, a.Backoff); err != nil {
					return 0, "", err
				}
				ok, err := retry()
				if err != nil || ok {
					return outPassed, "", err
				}
			}
		case "pause":
			r.notifyAll("paused", reason)
			for {
				r.pauseCmd = ""
				r.setState(StatePaused, reason)
				cond := func() bool { return r.pauseCmd != "" }
				got := true
				var err error
				if a.Timeout > 0 {
					got, err = workflow.AwaitWithTimeout(r.ctx, a.Timeout, cond)
				} else {
					err = workflow.Await(r.ctx, cond)
				}
				if err != nil {
					return 0, "", err
				}
				if !got {
					if a.OnTimeout == "abort" {
						return 0, "", errAbort
					}
					return outHeld, reason + " (pause timed out)", nil
				}
				cmd := r.pauseCmd
				r.pauseCmd = ""
				switch cmd {
				case SignalResume:
					return outResume, "", nil
				case SignalAbort:
					return 0, "", errAbort
				case SignalRetry:
					ok, err := retry()
					if err != nil || ok {
						return outPassed, "", err
					}
				}
			}
		case "hold":
			return outHeld, reason, nil
		}
	}
	return outHeld, reason, nil
}

func (r *runner) held(msg string) TargetResult {
	r.notifyAll("held", msg)
	return r.end(Held, StateHeld, msg)
}

func (r *runner) abort() TargetResult {
	r.ctx, _ = workflow.NewDisconnectedContext(r.ctx)
	r.notifyAll("aborted", "rollout aborted")
	return r.end(Aborted, StateAborted, "aborted")
}

func (r *runner) end(status, state, msg string) TargetResult {
	r.setState(state, msg)
	return TargetResult{Status: status, Message: msg}
}

// signalLoop drains operator signals as they arrive. Signals that do not
// apply to the current state are dropped, never left buffered.
func (r *runner) signalLoop() {
	log := workflow.GetLogger(r.ctx)
	ch := func(name string) workflow.ReceiveChannel { return workflow.GetSignalChannel(r.ctx, name) }
	approve, reject, resume, retry, abort, stop := ch(SignalApprove), ch(SignalReject), ch(SignalResume), ch(SignalRetry), ch(SignalAbort), ch(SignalStop)

	decide := func(s OperatorSignal, o plan.Outcome, name string) {
		if r.status.State != StateAwaitingApproval {
			log.Info("dropping signal: target not awaiting approval", "signal", name, "state", r.status.State)
			return
		}
		if s.Validation != "" {
			if !r.awaiting[s.Validation] {
				log.Info("dropping signal: validation not awaiting approval", "signal", name, "validation", s.Validation)
				return
			}
			r.approvals[s.Validation] = o
			return
		}
		for n := range r.awaiting {
			r.approvals[n] = o
		}
	}
	pauseCmd := func(name string) {
		if r.status.State != StatePaused || r.pauseCmd != "" {
			log.Info("dropping signal: target not paused", "signal", name, "state", r.status.State)
			return
		}
		r.pauseCmd = name
	}

	workflow.Go(r.ctx, func(ctx workflow.Context) {
		for ctx.Err() == nil {
			sel := workflow.NewSelector(ctx)
			sel.AddReceive(approve, func(c workflow.ReceiveChannel, _ bool) {
				var s OperatorSignal
				c.Receive(ctx, &s)
				decide(s, plan.PASS, SignalApprove)
			})
			sel.AddReceive(reject, func(c workflow.ReceiveChannel, _ bool) {
				var s OperatorSignal
				c.Receive(ctx, &s)
				decide(s, plan.FAIL, SignalReject)
			})
			for _, c := range []struct {
				ch   workflow.ReceiveChannel
				name string
			}{{resume, SignalResume}, {retry, SignalRetry}, {abort, SignalAbort}} {
				c := c
				sel.AddReceive(c.ch, func(ch workflow.ReceiveChannel, _ bool) {
					ch.Receive(ctx, nil)
					pauseCmd(c.name)
				})
			}
			sel.AddReceive(stop, func(c workflow.ReceiveChannel, _ bool) {
				c.Receive(ctx, nil)
				r.stop = true
			})
			sel.AddReceive(ctx.Done(), func(workflow.ReceiveChannel, bool) {})
			sel.Select(ctx)
		}
	})
}

func (r *runner) setState(state, msg string) {
	r.status.State = state
	r.status.Seq++
	if msg != "" || state == StateSucceeded {
		r.status.Message = msg
	}
	r.status.At = workflow.Now(r.ctx)
	if r.in.GlobalID == "" {
		return
	}
	snap := r.status
	snap.Verdicts = maps.Clone(r.status.Verdicts)
	snap.AwaitingApproval = append([]string(nil), r.status.AwaitingApproval...)
	sort.Strings(snap.AwaitingApproval)
	// Progress is best effort, but terminal states are waited on so the
	// signal is delivered before the target completes.
	f := workflow.SignalExternalWorkflow(r.ctx, r.in.GlobalID, "", SignalProgress, snap)
	switch state {
	case StateSucceeded, StateHeld, StateAborted:
		_ = f.Get(r.ctx, nil)
	}
}

func (r *runner) notify(notifier, kind, msg string) {
	if notifier == "" {
		return
	}
	ctx := workflow.WithActivityOptions(r.ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: 5 * time.Second, BackoffCoefficient: 2, MaximumAttempts: 3},
	})
	err := workflow.ExecuteActivity(ctx, "Notify", activities.NotifyInput{Notifier: notifier, Event: plugin.Event{
		RolloutID: r.in.GlobalID, Target: r.target(), Kind: kind, Message: msg,
	}}).Get(ctx, nil)
	if err != nil {
		workflow.GetLogger(r.ctx).Warn("notify failed", "notifier", notifier, "error", err)
	}
}

// notifyAll sends to every notifier named by the target's notify actions.
func (r *runner) notifyAll(kind, msg string) {
	for _, a := range r.t.OnFailure {
		if a.Kind == "notify" {
			r.notify(a.Notifier, kind, msg)
		}
	}
}

func (r *runner) activityCtx(pluginName string, def time.Duration, attempts int32) workflow.Context {
	timeout := r.in.Plugins[pluginName].Timeout
	if timeout <= 0 {
		timeout = def
	}
	return workflow.WithActivityOptions(r.ctx, workflow.ActivityOptions{
		StartToCloseTimeout: timeout,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: 5 * time.Second, BackoffCoefficient: 2, MaximumAttempts: attempts},
	})
}

func (r *runner) target() plugin.Target {
	return plugin.Target{Agent: r.in.Agent, Stage: r.t.Stage, Wave: r.t.Wave, Customer: r.t.Customer, Region: r.t.Region}
}

func (r *runner) canceled(err error) bool {
	return err != nil && (temporal.IsCanceledError(err) || r.ctx.Err() != nil)
}
