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

type goodRamper struct {
	pb.UnimplementedRamperServiceServer
	pct int32
}

func (r *goodRamper) SetPercent(_ context.Context, req *pb.SetPercentRequest) (*pb.SetPercentResponse, error) {
	r.pct = req.Percent
	return &pb.SetPercentResponse{Percent: r.pct, Version: req.Version}, nil
}

func (r *goodRamper) Current(context.Context, *pb.CurrentRequest) (*pb.CurrentResponse, error) {
	return &pb.CurrentResponse{Percent: r.pct}, nil
}

func (r *goodRamper) Describe(context.Context, *pb.DescribeRequest) (*pb.DescribeResponse, error) {
	return &pb.DescribeResponse{Name: "team/ramp", Kind: "ramper", Version: "1.0.0", ParamsJsonSchema: `{"type":"object"}`}, nil
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
	addr := listen(t, func(s *grpc.Server) { pb.RegisterRamperServiceServer(s, &goodRamper{}) })
	Ramper(t, addr)
}
