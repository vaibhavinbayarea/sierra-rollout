package workflows

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	"sierra-rollout/internal/activities"
	"sierra-rollout/internal/plan"
)

// stage builds a stage from waves of customers; every target uses the
// given steps, a 10m bake and the health validation.
func stage(steps []int, onFailure []plan.FailureAction, regions []string, waves ...plan.WavePlan) StageInput {
	base := input([]plan.ValidationCall{health}, onFailure...)
	for wi := range waves {
		for ci := range waves[wi].Customers {
			c := &waves[wi].Customers[ci]
			for _, r := range regions {
				t := base.Target
				t.Stage, t.Wave, t.Customer, t.Region = "prod", waves[wi].Name, c.Customer, r
				t.ID = "prod/" + waves[wi].Name + "/" + c.Customer + "/" + r
				t.Steps, t.Bake = steps, 10*time.Minute
				c.Targets = append(c.Targets, t)
			}
		}
	}
	return StageInput{GlobalID: "ro/a", Agent: "a", Version: "v1", Stage: plan.StagePlan{Name: "prod", Waves: waves}, Plugins: base.Plugins}
}

func wave(name string, maxParallel int, customers ...string) plan.WavePlan {
	w := plan.WavePlan{Name: name, MaxParallel: maxParallel}
	for _, c := range customers {
		w.Customers = append(w.Customers, plan.CustomerPlan{Customer: c})
	}
	return w
}

func (h *harness) runStage(in StageInput) StageResult {
	h.env.ExecuteWorkflow(StageRollout, in)
	require.True(h.t, h.env.IsWorkflowCompleted())
	require.NoError(h.t, h.env.GetWorkflowError())
	var res StageResult
	require.NoError(h.t, h.env.GetWorkflowResult(&res))
	return res
}

func TestStageSequentialRegions(t *testing.T) {
	h := newHarness(t, pass)
	res := h.runStage(stage([]int{50, 100}, []plan.FailureAction{hold}, []string{"a", "b"}, wave("w", 1, "c")))
	assert.Equal(t, Succeeded, res.Status)
	var regions []string
	for _, r := range h.ramps {
		regions = append(regions, r.Target.Region)
	}
	assert.Equal(t, []string{"a", "a", "b", "b"}, regions)
}

func TestStageWaveParallelism(t *testing.T) {
	h := newHarness(t, pass)
	start := h.env.Now()
	res := h.runStage(stage([]int{100}, []plan.FailureAction{hold}, []string{"r"}, wave("w", 2, "c1", "c2", "c3")))
	assert.Equal(t, Succeeded, res.Status)
	require.Len(t, h.rampTimes, 3)
	assert.Equal(t, start, h.rampTimes[0])
	assert.Equal(t, start, h.rampTimes[1])
	assert.GreaterOrEqual(t, h.rampTimes[2].Sub(start), 10*time.Minute, "third customer must wait for a slot")
}

func TestStageWavesSequential(t *testing.T) {
	h := newHarness(t, pass)
	start := h.env.Now()
	res := h.runStage(stage([]int{100}, []plan.FailureAction{hold}, []string{"r"}, wave("w1", 2, "c1"), wave("w2", 2, "c2")))
	assert.Equal(t, Succeeded, res.Status)
	require.Len(t, h.ramps, 2)
	assert.Equal(t, "c2", h.ramps[1].Target.Customer)
	assert.GreaterOrEqual(t, h.rampTimes[1].Sub(start), 10*time.Minute)
}

func TestStageTargetHeldStopsSiblings(t *testing.T) {
	h := newHarness(t, pass)
	// Customer a's ramp fails permanently; b is mid-bake when a is held.
	h.rampErr = func(in activities.RampInput) error {
		if in.Target.Customer == "a" {
			return temporal.NewNonRetryableApplicationError("ramp broken", "RampError", nil)
		}
		return nil
	}
	res := h.runStage(stage([]int{25, 100}, []plan.FailureAction{hold}, []string{"r"}, wave("w1", 2, "a", "b"), wave("w2", 1, "c")))
	assert.Equal(t, Held, res.Status)
	assert.Contains(t, res.Message, "prod/w1/a/r")
	var bPercents []int
	for _, r := range h.ramps {
		assert.NotEqual(t, "c", r.Target.Customer, "wave 2 must not start")
		if r.Target.Customer == "b" {
			bPercents = append(bPercents, r.Percent)
		}
	}
	assert.Equal(t, []int{25}, bPercents)
}
