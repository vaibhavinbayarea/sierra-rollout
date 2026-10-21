package remote

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"

	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/plugin"
	"sierra-rollout/internal/registry"
	pb "sierra-rollout/proto/plugin/v1"
)

type fakeValidator struct {
	pb.UnimplementedValidatorServiceServer
	resp  *pb.ValidateResponse
	err   error
	sleep time.Duration
	got   *pb.ValidateRequest
}

func (f *fakeValidator) Validate(ctx context.Context, r *pb.ValidateRequest) (*pb.ValidateResponse, error) {
	f.got = r
	if f.sleep > 0 {
		select {
		case <-time.After(f.sleep):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.resp, f.err
}

type fakeRamper struct {
	pb.UnimplementedRamperServiceServer
	pct int32
	ver string
}

func (f *fakeRamper) SetPercent(_ context.Context, r *pb.SetPercentRequest) (*pb.SetPercentResponse, error) {
	f.pct, f.ver = r.Percent, r.Version
	return &pb.SetPercentResponse{Percent: f.pct, Version: f.ver}, nil
}

func (f *fakeRamper) Current(context.Context, *pb.CurrentRequest) (*pb.CurrentResponse, error) {
	return &pb.CurrentResponse{Percent: f.pct, Version: f.ver}, nil
}

func serve(t *testing.T, register func(*grpc.Server)) []grpc.DialOption {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer()
	register(s)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
}

func newValidator(t *testing.T, f *fakeValidator, timeout time.Duration) plugin.Validator {
	t.Helper()
	opts := serve(t, func(s *grpc.Server) { pb.RegisterValidatorServiceServer(s, f) })
	impl, err := New(registry.Entry{Name: "team/v", Kind: registry.KindValidator, Endpoint: "passthrough:///bufnet", Timeout: timeout}, opts...)
	require.NoError(t, err)
	return impl.(plugin.Validator)
}

func TestValidateMapsVerdict(t *testing.T) {
	ev, _ := structpb.NewStruct(map[string]any{"p95": 1.2})
	f := &fakeValidator{resp: &pb.ValidateResponse{Outcome: pb.Outcome_OUTCOME_FAIL, Summary: "slow", Evidence: ev}}
	v := newValidator(t, f, time.Second)

	got, err := v.Validate(context.Background(), plugin.ValidateRequest{
		Target: plugin.Target{Agent: "a", Region: "r"}, Version: "v1", Step: 1, Percent: 50, Attempt: 2,
		Params: map[string]any{"threshold": 0.9},
	})
	require.NoError(t, err)
	assert.Equal(t, plan.Verdict{Outcome: plan.FAIL, Summary: "slow", Evidence: map[string]any{"p95": 1.2}}, got)
	assert.Equal(t, int32(2), f.got.Attempt)
	assert.Equal(t, "r", f.got.Target.Region)
	assert.Equal(t, 0.9, f.got.Params.AsMap()["threshold"])
}

func TestValidateTimeout(t *testing.T) {
	v := newValidator(t, &fakeValidator{sleep: time.Second, resp: &pb.ValidateResponse{Outcome: pb.Outcome_OUTCOME_PASS}}, 50*time.Millisecond)
	_, err := v.Validate(context.Background(), plugin.ValidateRequest{})
	assert.Error(t, err)
}

func TestValidateGRPCError(t *testing.T) {
	v := newValidator(t, &fakeValidator{err: status.Error(codes.Unavailable, "down")}, time.Second)
	_, err := v.Validate(context.Background(), plugin.ValidateRequest{})
	assert.Error(t, err)
}

func TestValidateUnspecifiedOutcome(t *testing.T) {
	v := newValidator(t, &fakeValidator{resp: &pb.ValidateResponse{}}, time.Second)
	_, err := v.Validate(context.Background(), plugin.ValidateRequest{})
	assert.ErrorContains(t, err, "unspecified")
}

func TestRamperRoundTrip(t *testing.T) {
	f := &fakeRamper{}
	opts := serve(t, func(s *grpc.Server) { pb.RegisterRamperServiceServer(s, f) })
	impl, err := New(registry.Entry{Name: "team/r", Kind: registry.KindRamper, Endpoint: "passthrough:///bufnet", Timeout: time.Second}, opts...)
	require.NoError(t, err)
	r := impl.(plugin.Ramper)

	res, err := r.SetPercent(context.Background(), plugin.RampRequest{Version: "v2", Percent: 25})
	require.NoError(t, err)
	assert.Equal(t, plugin.RampResult{Percent: 25, Version: "v2"}, res)
	pct, ver, err := r.Current(context.Background(), plugin.Target{})
	require.NoError(t, err)
	assert.Equal(t, 25, pct)
	assert.Equal(t, "v2", ver)
}
