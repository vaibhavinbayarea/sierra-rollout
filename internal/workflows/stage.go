package workflows

import (
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"sierra-rollout/internal/plan"
)

type StageInput struct {
	GlobalID string
	Agent    string
	Version  string
	Stage    plan.StagePlan
	Plugins  map[string]plan.PluginRef
}

type StageResult struct {
	Status  string
	Message string
}

// StageRollout runs waves in order; within a wave customers run in
// parallel (bounded by MaxParallel) and each customer's regions in order.
// The first non-successful target stops the stage: no new targets start
// and running siblings stop after their current step.
func StageRollout(ctx workflow.Context, in StageInput) (StageResult, error) {
	res := StageResult{Status: Succeeded}
	stopping := false
	running := map[string]workflow.ChildWorkflowFuture{}

	finish := func(t plan.TargetPlan, tr TargetResult) {
		res.Status = Worst(res.Status, tr.Status)
		if tr.Status == Succeeded || stopping {
			return
		}
		stopping = true
		res.Message = t.ID + ": " + tr.Message
		for _, f := range running {
			f.SignalChildWorkflow(ctx, SignalStop, nil)
		}
	}

	for _, w := range in.Stage.Waves {
		if stopping {
			break
		}
		slots := workflow.NewBufferedChannel(ctx, max(w.MaxParallel, 1))
		wg := workflow.NewWaitGroup(ctx)
		for _, c := range w.Customers {
			wg.Add(1)
			workflow.Go(ctx, func(ctx workflow.Context) {
				defer wg.Done()
				slots.Send(ctx, struct{}{})
				defer slots.Receive(ctx, nil)
				for _, t := range c.Targets {
					if stopping {
						return
					}
					if ctx.Err() != nil {
						finish(t, TargetResult{Status: Aborted, Message: "aborted"})
						return
					}
					cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
						WorkflowID:          plan.TargetWorkflowID(in.GlobalID, t),
						WaitForCancellation: true,
					})
					f := workflow.ExecuteChildWorkflow(cctx, TargetRollout, TargetInput{
						GlobalID: in.GlobalID, Agent: in.Agent, Version: in.Version, Target: t, Plugins: in.Plugins,
					})
					running[t.ID] = f
					var tr TargetResult
					err := f.Get(ctx, &tr)
					delete(running, t.ID)
					if err != nil {
						tr = TargetResult{Status: Held, Message: err.Error()}
						if temporal.IsCanceledError(err) {
							tr = TargetResult{Status: Aborted, Message: "aborted"}
						}
					}
					finish(t, tr)
					if tr.Status != Succeeded {
						return
					}
				}
			})
		}
		wg.Wait(ctx)
	}
	return res, nil
}
