// Package plugincontract is a test kit plugin teams run against their
// plugin.v1 gRPC implementation:
//
//	func TestContract(t *testing.T) { plugincontract.Validator(t, "localhost:9000", sampleParams) }
package plugincontract

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"

	pb "sierra-rollout/proto/plugin/v1"
)

const callTimeout = 10 * time.Second

func dial(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), callTimeout)
	t.Cleanup(cancel)
	return c
}

func checkDescribe(t *testing.T, d *pb.DescribeResponse, err error, kind string) {
	t.Helper()
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if d.Kind != kind {
		t.Errorf("Describe kind = %q, want %q", d.Kind, kind)
	}
	if d.Name == "" || d.Version == "" {
		t.Errorf("Describe must return name and version, got %q %q", d.Name, d.Version)
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(d.ParamsJsonSchema))
	if err != nil {
		t.Fatalf("params_json_schema is not JSON: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("mem://params.json", doc); err != nil {
		t.Fatalf("params_json_schema: %v", err)
	}
	if _, err := c.Compile("mem://params.json"); err != nil {
		t.Errorf("params_json_schema does not compile: %v", err)
	}
}

// Validator checks a ValidatorService at addr.
func Validator(t *testing.T, addr string, sampleParams map[string]any) {
	c := pb.NewValidatorServiceClient(dial(t, addr))
	t.Run("Describe", func(t *testing.T) {
		d, err := c.Describe(ctx(t), &pb.DescribeRequest{})
		checkDescribe(t, d, err, "validator")
	})
	t.Run("ValidateIsRepeatable", func(t *testing.T) {
		params, err := structpb.NewStruct(sampleParams)
		if err != nil {
			t.Fatalf("sample params: %v", err)
		}
		for attempt := int32(1); attempt <= 2; attempt++ {
			resp, err := c.Validate(ctx(t), &pb.ValidateRequest{
				Target: &pb.Target{Agent: "contract-test", Stage: "staging", Wave: "default", Customer: "_", Region: "test"},
				Version: "v0", Percent: 10, Attempt: attempt, Params: params,
			})
			if err != nil {
				t.Fatalf("Validate attempt %d: %v", attempt, err)
			}
			if resp.Outcome == pb.Outcome_OUTCOME_UNSPECIFIED {
				t.Errorf("Validate attempt %d returned OUTCOME_UNSPECIFIED", attempt)
			}
		}
	})
}

// Ramper checks a RamperService at addr, including SetPercent idempotency.
func Ramper(t *testing.T, addr string) {
	c := pb.NewRamperServiceClient(dial(t, addr))
	var modes []string
	t.Run("Describe", func(t *testing.T) {
		d, err := c.Describe(ctx(t), &pb.DescribeRequest{})
		checkDescribe(t, d, err, "ramper")
		if d != nil {
			modes = d.Modes
		}
	})
	t.Run("SetPercentIsIdempotent", func(t *testing.T) { checkIdempotent(t, c, pb.RampMode_RAMP_MODE_LIVE, 10) })
	if slices.Contains(modes, "shadow") {
		// Shadow state must be independent of live state.
		t.Run("ShadowSetPercentIsIdempotent", func(t *testing.T) { checkIdempotent(t, c, pb.RampMode_RAMP_MODE_SHADOW, 30) })
		t.Run("LiveUnaffectedByShadow", func(t *testing.T) {
			cur, err := c.Current(ctx(t), &pb.CurrentRequest{Target: contractTarget, Mode: pb.RampMode_RAMP_MODE_LIVE})
			if err != nil {
				t.Fatalf("Current: %v", err)
			}
			if cur.Percent != 10 {
				t.Errorf("live percent = %d after shadow SetPercent(30), want 10", cur.Percent)
			}
		})
	}
}

var contractTarget = &pb.Target{Agent: "contract-test", Stage: "staging", Wave: "default", Customer: "_", Region: "test"}

func checkIdempotent(t *testing.T, c pb.RamperServiceClient, mode pb.RampMode, pct int32) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if _, err := c.SetPercent(ctx(t), &pb.SetPercentRequest{Target: contractTarget, Version: "v0", Percent: pct, Mode: mode}); err != nil {
			t.Fatalf("SetPercent(%s) call %d: %v", mode, i+1, err)
		}
	}
	cur, err := c.Current(ctx(t), &pb.CurrentRequest{Target: contractTarget, Mode: mode})
	if err != nil {
		t.Fatalf("Current(%s): %v", mode, err)
	}
	if cur.Percent != pct {
		t.Errorf("Current(%s) percent = %d after SetPercent(%d) twice, want %d", mode, cur.Percent, pct, pct)
	}
}
