package cli

import (
	"bytes"
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/spec"
	"sierra-rollout/internal/workflows"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestRenderStatusGolden(t *testing.T) {
	st := &workflows.RolloutStatus{
		ID: "ro/support-agent", Version: "v1.4.2", State: workflows.StateRunning,
		Order: []string{"staging/default/_/us-east-1", "prod/canary/acme/us-east-1", "prod/broad/globex/us-east-1"},
		Targets: map[string]workflows.TargetStatus{
			"staging/default/_/us-east-1": {Stage: "staging", Wave: "default", Customer: "_", Region: "us-east-1",
				State: workflows.StateSucceeded, Step: 1, Steps: 1, Percent: 100},
			"prod/canary/acme/us-east-1": {Stage: "prod", Wave: "canary", Customer: "acme", Region: "us-east-1",
				State: workflows.StateAwaitingApproval, Step: 4, Steps: 4, Percent: 100, AwaitingApproval: []string{"signoff"}},
			"prod/broad/globex/us-east-1": {Stage: "prod", Wave: "broad", Customer: "globex", Region: "us-east-1",
				State: workflows.StatePaused, Step: 1, Steps: 2, Percent: 25,
				Verdicts: map[string]plan.Verdict{"error-rate": {Outcome: plan.FAIL}}, Message: "failed: error-rate"},
		},
	}
	var buf bytes.Buffer
	RenderStatus(&buf, st)
	golden := "testdata/status.golden"
	if *update {
		require.NoError(t, os.WriteFile(golden, buf.Bytes(), 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	assert.Equal(t, string(want), buf.String())
	assert.Contains(t, buf.String(), "awaiting: signoff")
	assert.Contains(t, buf.String(), "step 1/2 @ 25%")
}

func TestRenderDiagnostics(t *testing.T) {
	var buf bytes.Buffer
	RenderDiagnostics(&buf, "policy.yaml", []spec.Diagnostic{{Path: "stages[2].waves[1].steps", Line: 41, Message: "steps must end at 100"}})
	assert.Equal(t, "policy.yaml:41: stages[2].waves[1].steps: steps must end at 100\n", buf.String())
}

func TestRenderPlan(t *testing.T) {
	p := &plan.Plan{ID: "ro/a", Version: "v1", Stages: []plan.StagePlan{{Name: "prod", Waves: []plan.WavePlan{{
		Name: "broad", MaxParallel: 2, Customers: []plan.CustomerPlan{{Customer: "acme", Targets: []plan.TargetPlan{{
			Region: "us-east-1", Steps: []int{25, 100}, Validations: []plan.ValidationCall{{Name: "health"}, {Name: "signoff", When: "final_step"}},
		}}}},
	}}}}}
	var buf bytes.Buffer
	RenderPlan(&buf, p)
	out := buf.String()
	assert.Contains(t, out, "ro/a  v1")
	assert.Contains(t, out, "broad (max_parallel 2)")
	assert.Contains(t, out, "us-east-1  steps [25 100]")
	assert.Contains(t, out, "validate: health, signoff (final step)")
}
