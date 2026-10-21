package plugincontract

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "sierra-rollout/proto/plugin/v1"
)

type goodValidator struct {
	pb.UnimplementedValidatorServiceServer
}

func (goodValidator) Validate(context.Context, *pb.ValidateRequest) (*pb.ValidateResponse, error) {
	return &pb.ValidateResponse{Outcome: pb.Outcome_OUTCOME_PASS}, nil
}

func (goodValidator) Describe(context.Context, *pb.DescribeRequest) (*pb.DescribeResponse, error) {
	return &pb.DescribeResponse{Name: "team/good", Kind: "validator", Version: "1.0.0",
		ParamsJsonSchema: `{"type":"object","properties":{"threshold":{"type":"number"}}}`}, nil
}

// goodRamper keeps percent per mode; modes lists what Describe advertises.
type goodRamper struct {
	pb.UnimplementedRamperServiceServer
	pct      map[pb.RampMode]int32
	modes    []string
	sawModes map[pb.RampMode]bool
}

func newRamper(modes ...string) *goodRamper {
	return &goodRamper{pct: map[pb.RampMode]int32{}, modes: modes, sawModes: map[pb.RampMode]bool{}}
}

func norm(m pb.RampMode) pb.RampMode {
	if m == pb.RampMode_RAMP_MODE_UNSPECIFIED {
		return pb.RampMode_RAMP_MODE_LIVE
	}
	return m
}

func (r *goodRamper) SetPercent(_ context.Context, req *pb.SetPercentRequest) (*pb.SetPercentResponse, error) {
	r.sawModes[norm(req.Mode)] = true
	r.pct[norm(req.Mode)] = req.Percent
	return &pb.SetPercentResponse{Percent: req.Percent, Version: req.Version}, nil
}

func (r *goodRamper) Current(_ context.Context, req *pb.CurrentRequest) (*pb.CurrentResponse, error) {
	return &pb.CurrentResponse{Percent: r.pct[norm(req.Mode)]}, nil
}

func (r *goodRamper) Describe(context.Context, *pb.DescribeRequest) (*pb.DescribeResponse, error) {
	return &pb.DescribeResponse{Name: "team/ramp", Kind: "ramper", Version: "1.0.0", ParamsJsonSchema: `{"type":"object"}`, Modes: r.modes}, nil
}

func listen(t *testing.T, register func(*grpc.Server)) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := grpc.NewServer()
	register(s)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

func TestValidatorContract(t *testing.T) {
	addr := listen(t, func(s *grpc.Server) { pb.RegisterValidatorServiceServer(s, goodValidator{}) })
	Validator(t, addr, map[string]any{"threshold": 0.5})
}

func TestRamperContract(t *testing.T) {
	r := newRamper()
	addr := listen(t, func(s *grpc.Server) { pb.RegisterRamperServiceServer(s, r) })
	Ramper(t, addr)
	if r.sawModes[pb.RampMode_RAMP_MODE_SHADOW] {
		t.Error("shadow checked although Describe did not advertise it")
	}
}

func TestRamperContractShadow(t *testing.T) {
	r := newRamper("live", "shadow")
	addr := listen(t, func(s *grpc.Server) { pb.RegisterRamperServiceServer(s, r) })
	Ramper(t, addr)
	if !r.sawModes[pb.RampMode_RAMP_MODE_SHADOW] {
		t.Error("shadow not checked although Describe advertises it")
	}
}
