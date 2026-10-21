package workflows

import (
	"go.temporal.io/sdk/worker"

	"sierra-rollout/internal/activities"
)

// Register registers the rollout workflows and activities on a worker.
func Register(w worker.Registry, a *activities.Activities) {
	w.RegisterWorkflow(GlobalRollout)
	w.RegisterWorkflow(StageRollout)
	w.RegisterWorkflow(TargetRollout)
	w.RegisterActivity(a)
}
