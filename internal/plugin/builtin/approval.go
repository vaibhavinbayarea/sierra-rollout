package builtin

import (
	"context"

	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/plugin"
)

// approval always defers to an operator signal.
type approval struct{}

func (approval) Validate(_ context.Context, r plugin.ValidateRequest) (plan.Verdict, error) {
	msg, _ := r.Params["message"].(string)
	if msg == "" {
		msg = "awaiting approval"
	}
	return plan.Verdict{Outcome: plan.AWAIT_SIGNAL, Summary: msg}, nil
}
