// Package builtin provides the in-process plugins. In v1 they are stubs
// whose verdicts are scripted through params.stub.
package builtin

import (
	"context"

	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/plugin"
)

// Register returns every built-in implementation by registry name.
func Register() map[string]any {
	return map[string]any{
		"prometheus":      stubValidator{},
		"http-health":     stubValidator{},
		"manual-approval": approval{},
		"mock-ramper":     NewMockRamper(),
		"log-notifier":    logNotifier{},
	}
}

// stubValidator returns params.stub.outcome while Attempt < after_attempt
// (always, when after_attempt is unset), and PASS otherwise.
type stubValidator struct{}

func (stubValidator) Validate(_ context.Context, r plugin.ValidateRequest) (plan.Verdict, error) {
	outcome := plan.PASS
	if stub, ok := r.Params["stub"].(map[string]any); ok {
		after, hasAfter := toInt(stub["after_attempt"])
		if o, ok := stub["outcome"].(string); ok && (!hasAfter || r.Attempt < after) {
			outcome = plan.Outcome(o)
		}
	}
	return plan.Verdict{Outcome: outcome, Summary: "stub: " + string(outcome)}, nil
}

// toInt accepts YAML ints and JSON floats.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}
