// Package plugin defines the ramper, validator and notifier contracts that
// built-in and remote plugins implement.
package plugin

import (
	"context"

	"sierra-rollout/internal/plan"
)

// Target identifies the unit being rolled out, so plugins can scope queries.
type Target struct {
	Agent    string `json:"agent"`
	Stage    string `json:"stage"`
	Wave     string `json:"wave"`
	Customer string `json:"customer"`
	Region   string `json:"region"`
}

type RampRequest struct {
	Target  Target
	Version string
	Percent int
	Params  map[string]any
}

type RampResult struct {
	Percent int
	Version string
}

type ValidateRequest struct {
	Target  Target
	Version string
	Step    int // 0-based
	Percent int
	Attempt int // 1-based
	Params  map[string]any
}

type Event struct {
	RolloutID string         `json:"rollout_id"`
	Target    Target         `json:"target"`
	Kind      string         `json:"kind"` // failed | paused | held | aborted | warning
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
}

// Ramper moves traffic. SetPercent must be idempotent and absolute.
type Ramper interface {
	SetPercent(ctx context.Context, r RampRequest) (RampResult, error)
	Current(ctx context.Context, t Target) (percent int, version string, err error)
}

// Validator is stateless; long waits are expressed as AWAIT_SIGNAL.
type Validator interface {
	Validate(ctx context.Context, r ValidateRequest) (plan.Verdict, error)
}

type Notifier interface {
	Notify(ctx context.Context, e Event) error
}
