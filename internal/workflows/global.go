package workflows

import (
	"errors"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"sierra-rollout/internal/plan"
)

// stageBusyRetry is how often a pipelined rollout re-tries a busy stage.
const stageBusyRetry = 30 * time.Second

// GlobalRollout runs the plan's stages in order and maintains the status
// tree served by the status query.
func GlobalRollout(ctx workflow.Context, p plan.Plan) (RolloutStatus, error) {
	st := RolloutStatus{ID: p.ID, Agent: p.Agent, Version: p.Version, State: StateRunning, Targets: map[string]TargetStatus{}}
	for _, t := range p.Targets() {
		st.Order = append(st.Order, t.ID)
		st.Targets[t.ID] = TargetStatus{ID: t.ID, Stage: t.Stage, Wave: t.Wave, Customer: t.Customer, Region: t.Region,
			State: StatePending, Steps: len(t.Steps)}
	}
	if err := workflow.SetQueryHandler(ctx, QueryStatus, func() (RolloutStatus, error) { return st, nil }); err != nil {
		return st, err
	}

	progress := workflow.GetSignalChannel(ctx, SignalProgress)
	apply := func(c workflow.ReceiveChannel) {
		var ts TargetStatus
		c.Receive(ctx, &ts)
		st.Targets[ts.ID] = ts
	}
	stagesCtx, cancel := workflow.WithCancel(ctx)
	aborted := false
	workflow.Go(ctx, func(ctx workflow.Context) {
		abort := workflow.GetSignalChannel(ctx, SignalAbort)
		for {
			sel := workflow.NewSelector(ctx)
			sel.AddReceive(progress, func(c workflow.ReceiveChannel, _ bool) { apply(c) })
			sel.AddReceive(abort, func(c workflow.ReceiveChannel, _ bool) {
				c.Receive(ctx, nil)
				aborted = true
				cancel()
			})
			sel.Select(ctx)
		}
	})

	result := Succeeded
	for _, s := range p.Stages {
		res, err := runStage(ctx, stagesCtx, p, s, &st)
		if err != nil {
			return st, err
		}
		result = Worst(result, res.Status)
		if res.Message != "" {
			st.Message = s.Name + ": " + res.Message
		}
		if result != Succeeded {
			break
		}
	}
	if aborted {
		result = Aborted
	}
	// Apply progress signals that arrived after the last stage finished.
	for progress.ReceiveAsync(nil) {
	}
	st.State, st.WaitingForStage = result, ""
	return st, nil
}

// runStage starts the stage child, waiting while another rollout holds the
// stage lock.
func runStage(ctx, stagesCtx workflow.Context, p plan.Plan, s plan.StagePlan, st *RolloutStatus) (StageResult, error) {
	for {
		if stagesCtx.Err() != nil {
			return StageResult{Status: Aborted}, nil
		}
		cctx := workflow.WithChildOptions(stagesCtx, workflow.ChildWorkflowOptions{
			WorkflowID:            plan.StageID(p.Agent, s.Name),
			WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
			WaitForCancellation:   true,
		})
		f := workflow.ExecuteChildWorkflow(cctx, StageRollout, StageInput{
			GlobalID: p.ID, Agent: p.Agent, Version: p.Version, Stage: s, Plugins: p.Plugins,
		})
		if err := f.GetChildWorkflowExecution().Get(ctx, nil); err != nil {
			var busy *temporal.ChildWorkflowExecutionAlreadyStartedError
			if !errors.As(err, &busy) {
				if temporal.IsCanceledError(err) {
					return StageResult{Status: Aborted}, nil
				}
				return StageResult{}, err
			}
			st.State, st.WaitingForStage = StateWaitingForStage, s.Name
			if err := workflow.Sleep(stagesCtx, stageBusyRetry); err != nil {
				return StageResult{Status: Aborted}, nil
			}
			continue
		}
		st.State, st.WaitingForStage = StateRunning, ""
		var res StageResult
		if err := f.Get(ctx, &res); err != nil {
			if temporal.IsCanceledError(err) {
				return StageResult{Status: Aborted}, nil
			}
			return StageResult{}, err
		}
		return res, nil
	}
}
