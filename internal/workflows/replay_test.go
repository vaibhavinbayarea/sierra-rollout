package workflows

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/worker"
)

// TestReplay replays recorded production-shaped histories (see
// test/integration -record) to catch non-deterministic workflow changes.
const replayRuns = 20

func TestReplay(t *testing.T) {
	files, err := filepath.Glob("testdata/histories/*.json")
	require.NoError(t, err)
	require.NotEmpty(t, files, "record histories with: go test -tags integration ./test/integration -run TestE2EHappyPath -args -record")
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			// Repeat so order-dependent nondeterminism (e.g. map iteration
			// while issuing commands) shows up reliably.
			for i := 0; i < replayRuns; i++ {
				r := worker.NewWorkflowReplayer()
				r.RegisterWorkflow(GlobalRollout)
				r.RegisterWorkflow(StageRollout)
				r.RegisterWorkflow(TargetRollout)
				require.NoError(t, r.ReplayWorkflowHistoryFromJSONFile(nil, f), "replay %d", i)
			}
		})
	}
}
