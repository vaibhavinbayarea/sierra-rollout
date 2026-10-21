// Package remote adapts plugin.v1 gRPC services to the plugin interfaces.
package remote

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"

	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/plugin"
	"sierra-rollout/internal/registry"
	pb "sierra-rollout/proto/plugin/v1"
)

// New returns a plugin.Ramper, plugin.Validator or plugin.Notifier for e.
// With no opts the connection is plaintext; pass credentials to add auth.
func New(e registry.Entry, opts ...grpc.DialOption) (any, error) {
	if len(opts) == 0 {
		opts = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	}
	conn, err := grpc.NewClient(e.Endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", e.Endpoint, err)
	}
	base := client{timeout: e.Timeout}
	switch e.Kind {
	case registry.KindRamper:
		return &ramper{client: base, c: pb.NewRamperServiceClient(conn)}, nil
	case registry.KindValidator:
		return &validator{client: base, c: pb.NewValidatorServiceClient(conn)}, nil
	case registry.KindNotifier:
		return &notifier{client: base, c: pb.NewNotifierServiceClient(conn)}, nil
	}
	return nil, fmt.Errorf("unknown kind %q", e.Kind)
}

type client struct{ timeout time.Duration }

func (c client) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.timeout)
}

type ramper struct {
	client
	c pb.RamperServiceClient
}

func (r *ramper) SetPercent(ctx context.Context, req plugin.RampRequest) (plugin.RampResult, error) {
	ctx, cancel := r.ctx(ctx)
	defer cancel()
	params, err := toStruct(req.Params)
	if err != nil {
		return plugin.RampResult{}, err
	}
	resp, err := r.c.SetPercent(ctx, &pb.SetPercentRequest{Target: toTarget(req.Target), Version: req.Version, Percent: int32(req.Percent), Params: params})
	if err != nil {
		return plugin.RampResult{}, err
	}
	return plugin.RampResult{Percent: int(resp.Percent), Version: resp.Version}, nil
}

func (r *ramper) Current(ctx context.Context, t plugin.Target) (int, string, error) {
	ctx, cancel := r.ctx(ctx)
	defer cancel()
	resp, err := r.c.Current(ctx, &pb.CurrentRequest{Target: toTarget(t)})
	if err != nil {
		return 0, "", err
	}
	return int(resp.Percent), resp.Version, nil
}

type validator struct {
	client
	c pb.ValidatorServiceClient
}

var outcomes = map[pb.Outcome]plan.Outcome{
	pb.Outcome_OUTCOME_PASS:         plan.PASS,
	pb.Outcome_OUTCOME_FAIL:         plan.FAIL,
	pb.Outcome_OUTCOME_INCONCLUSIVE: plan.INCONCLUSIVE,
	pb.Outcome_OUTCOME_AWAIT_SIGNAL: plan.AWAIT_SIGNAL,
}

func (v *validator) Validate(ctx context.Context, req plugin.ValidateRequest) (plan.Verdict, error) {
	ctx, cancel := v.ctx(ctx)
	defer cancel()
	params, err := toStruct(req.Params)
	if err != nil {
		return plan.Verdict{}, err
	}
	resp, err := v.c.Validate(ctx, &pb.ValidateRequest{
		Target: toTarget(req.Target), Version: req.Version, Step: int32(req.Step),
		Percent: int32(req.Percent), Attempt: int32(req.Attempt), Params: params,
	})
	if err != nil {
		return plan.Verdict{}, err
	}
	o, ok := outcomes[resp.Outcome]
	if !ok {
		return plan.Verdict{}, fmt.Errorf("plugin returned unspecified outcome")
	}
	var ev map[string]any
	if resp.Evidence != nil {
		ev = resp.Evidence.AsMap()
	}
	return plan.Verdict{Outcome: o, Summary: resp.Summary, Evidence: ev}, nil
}

type notifier struct {
	client
	c pb.NotifierServiceClient
}

func (n *notifier) Notify(ctx context.Context, e plugin.Event) error {
	ctx, cancel := n.ctx(ctx)
	defer cancel()
	details, err := toStruct(e.Details)
	if err != nil {
		return err
	}
	_, err = n.c.Notify(ctx, &pb.NotifyRequest{RolloutId: e.RolloutID, Target: toTarget(e.Target), Kind: e.Kind, Message: e.Message, Details: details})
	return err
}

func toTarget(t plugin.Target) *pb.Target {
	return &pb.Target{Agent: t.Agent, Stage: t.Stage, Wave: t.Wave, Customer: t.Customer, Region: t.Region}
}

func toStruct(m map[string]any) (*structpb.Struct, error) {
	if m == nil {
		return nil, nil
	}
	return structpb.NewStruct(m)
}
