package workflows

import (
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"sierra-rollout/internal/activities"
	"sierra-rollout/internal/plugin"
	"sierra-rollout/internal/plugin/builtin"
	"sierra-rollout/internal/plugin/remote"
	"sierra-rollout/internal/registry"
)

// TaskQueue is the Temporal task queue the rollout worker polls.
const TaskQueue = "rollout"

// Register registers the rollout workflows and activities on a worker.
func Register(w worker.Registry, a *activities.Activities) {
	w.RegisterWorkflow(GlobalRollout)
	w.RegisterWorkflow(StageRollout)
	w.RegisterWorkflow(TargetRollout)
	w.RegisterActivity(a)
}

// NewWorker builds a worker with built-in and remote plugins bound from reg.
func NewWorker(c client.Client, reg *registry.Registry) (worker.Worker, error) {
	plugins, err := plugin.NewSet(reg, builtin.Register(), func(e registry.Entry) (any, error) { return remote.New(e) })
	if err != nil {
		return nil, err
	}
	w := worker.New(c, TaskQueue, worker.Options{})
	Register(w, &activities.Activities{Plugins: plugins})
	return w, nil
}
