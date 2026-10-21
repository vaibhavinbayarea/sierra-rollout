package builtin

import (
	"context"
	"log/slog"

	"sierra-rollout/internal/plugin"
)

type logNotifier struct{}

func (logNotifier) Notify(_ context.Context, e plugin.Event) error {
	slog.Info("rollout event", "rollout", e.RolloutID, "kind", e.Kind, "target", e.Target, "message", e.Message, "details", e.Details)
	return nil
}
