// Package activities wraps plugin calls as Temporal activities.
package activities

import (
	"context"
	"errors"

	"go.temporal.io/sdk/temporal"

	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/plugin"
)

// ErrTypePluginMissing marks a non-retryable error for a plugin that the
// worker cannot resolve.
const ErrTypePluginMissing = "PluginMissing"

type Activities struct {
	Plugins *plugin.Set
}

type RampInput struct {
	Ramper  plan.PluginCall
	Target  plugin.Target
	Version string
	Percent int
}

type ValidateInput struct {
	Validation plan.ValidationCall
	Target     plugin.Target
	Version    string
	Step       int
	Percent    int
	Attempt    int
}

type NotifyInput struct {
	Notifier string
	Event    plugin.Event
}

func (a *Activities) Ramp(ctx context.Context, in RampInput) error {
	r, err := a.Plugins.Ramper(in.Ramper.Plugin)
	if err != nil {
		return missing(err)
	}
	_, err = r.SetPercent(ctx, plugin.RampRequest{Target: in.Target, Version: in.Version, Percent: in.Percent, Params: in.Ramper.Params})
	return err
}

func (a *Activities) Validate(ctx context.Context, in ValidateInput) (plan.Verdict, error) {
	v, err := a.Plugins.Validator(in.Validation.Plugin)
	if err != nil {
		return plan.Verdict{}, missing(err)
	}
	return v.Validate(ctx, plugin.ValidateRequest{
		Target: in.Target, Version: in.Version, Step: in.Step, Percent: in.Percent,
		Attempt: in.Attempt, Params: in.Validation.Params,
	})
}

func (a *Activities) Notify(ctx context.Context, in NotifyInput) error {
	n, err := a.Plugins.Notifier(in.Notifier)
	if err != nil {
		return missing(err)
	}
	return n.Notify(ctx, in.Event)
}

func missing(err error) error {
	if errors.Is(err, plugin.ErrPluginMissing) {
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypePluginMissing, err)
	}
	return err
}
