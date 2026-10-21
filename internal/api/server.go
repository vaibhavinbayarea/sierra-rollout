// Package api serves the rollout HTTP API.
package api

import (
	"net/http"

	"go.temporal.io/sdk/client"

	"sierra-rollout/internal/registry"
	"sierra-rollout/internal/workflows"
)

// TaskQueue is the Temporal task queue the rollout worker polls.
const TaskQueue = workflows.TaskQueue

type server struct {
	temporal  client.Client
	reg       *registry.Registry
	taskQueue string
}

// NewServer returns the API handler. Rollout and target IDs contain '/', so
// clients send them path-escaped as single segments.
func NewServer(c client.Client, reg *registry.Registry, taskQueue string) http.Handler {
	s := &server{temporal: c, reg: reg, taskQueue: taskQueue}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/plans", s.plan)
	mux.HandleFunc("POST /v1/rollouts", s.start)
	mux.HandleFunc("GET /v1/rollouts", s.listRollouts)
	mux.HandleFunc("GET /ui", s.ui)
	mux.HandleFunc("GET /v1/rollouts/{id}", s.status)
	mux.HandleFunc("POST /v1/rollouts/{id}/targets/{tid}/signal", s.signal)
	mux.HandleFunc("POST /v1/rollouts/{id}/abort", s.abort)
	return mux
}
