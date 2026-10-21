//go:build integration

// Package integration runs the API, worker and workflows against a real
// Temporal dev server (downloaded automatically by the SDK test suite).
package integration

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	historypb "go.temporal.io/api/history/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"

	"go.temporal.io/sdk/worker"

	"sierra-rollout/internal/activities"
	"sierra-rollout/internal/api"
	"sierra-rollout/internal/cli"
	"sierra-rollout/internal/compiler"
	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/plugin"
	"sierra-rollout/internal/plugin/builtin"
	"sierra-rollout/internal/registry"
	"sierra-rollout/internal/workflows"
)

var record = flag.Bool("record", false, "write happy-path workflow histories for replay tests")

var (
	temporal client.Client
	rollouts *cli.Client
)

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	srv, err := testsuite.StartDevServer(ctx, testsuite.DevServerOptions{LogLevel: "error"})
	if err != nil {
		fmt.Fprintln(os.Stderr, "start dev server:", err)
		return 1
	}
	defer func() { _ = srv.Stop() }()
	temporal = srv.Client()

	reg, err := registry.Load("../../plugins.yaml")
	if err != nil {
		fmt.Fprintln(os.Stderr, "registry:", err)
		return 1
	}
	w, err := workflows.NewWorker(temporal, reg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		return 1
	}
	if err := w.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "worker start:", err)
		return 1
	}
	defer w.Stop()

	httpSrv := httptest.NewServer(api.NewServer(temporal, reg, api.TaskQueue))
	defer httpSrv.Close()
	rollouts = &cli.Client{BaseURL: httpSrv.URL, HTTP: httpSrv.Client()}
	return m.Run()
}

// policy loads an example with the agent renamed and string edits applied.
func policy(t *testing.T, file, agent string, edits ...string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../examples/" + file)
	require.NoError(t, err)
	s := strings.Replace(string(b), "agent: support-agent", "agent: "+agent, 1)
	s = strings.ReplaceAll(s, "bake: 10s", "bake: 1s")
	for i := 0; i+1 < len(edits); i += 2 {
		require.Contains(t, s, edits[i])
		s = strings.Replace(s, edits[i], edits[i+1], 1)
	}
	return []byte(s)
}

// minimal is a one-stage policy for lock tests.
func minimal(agent, bake string, pipelining bool) []byte {
	return []byte(fmt.Sprintf(`apiVersion: rollout/v1
agent: %s
ramper: { plugin: mock-ramper }
concurrency: { pipelining: %t }
defaults: { bake: %s, steps: [100] }
stages:
  - { name: staging, regions: [us-east-1] }
`, agent, pipelining, bake))
}

func start(t *testing.T, src []byte, version string) string {
	t.Helper()
	id, diags, err := rollouts.Start(context.Background(), src, version)
	require.NoError(t, err)
	require.Empty(t, diags)
	return id
}

// drive polls status until the rollout finishes, approving awaiting targets
// and retrying paused ones. It returns the final status and every target
// state observed.
func drive(t *testing.T, id string, timeout time.Duration) (*workflows.RolloutStatus, map[string]bool) {
	t.Helper()
	seen := map[string]bool{}
	deadline := time.Now().Add(timeout)
	ctx := context.Background()
	for time.Now().Before(deadline) {
		st, err := rollouts.Status(ctx, id)
		require.NoError(t, err)
		for tid, ts := range st.Targets {
			seen[ts.State] = true
			switch ts.State {
			case workflows.StateAwaitingApproval:
				_ = rollouts.Signal(ctx, id, tid, workflows.SignalApprove, "")
			case workflows.StatePaused:
				_ = rollouts.Signal(ctx, id, tid, workflows.SignalRetry, "")
			}
		}
		switch st.State {
		case workflows.Succeeded, workflows.Held, workflows.Aborted:
			return st, seen
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("rollout %s did not finish within %s", id, timeout)
	return nil, nil
}

func waitFor(t *testing.T, id string, timeout time.Duration, cond func(*workflows.RolloutStatus) bool) *workflows.RolloutStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := rollouts.Status(context.Background(), id)
		if err == nil && cond(st) {
			return st
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("condition on %s not met within %s", id, timeout)
	return nil
}

func TestE2EHappyPath(t *testing.T) {
	id := start(t, policy(t, "policy.yaml", "happy"), "v1")
	assert.Equal(t, "ro/happy", id)
	st, seen := drive(t, id, 3*time.Minute)
	assert.Equal(t, workflows.Succeeded, st.State)
	assert.True(t, seen[workflows.StateAwaitingApproval], "prod targets wait for signoff")
	for _, tid := range st.Order {
		assert.Equal(t, workflows.StateSucceeded, st.Targets[tid].State, tid)
	}
	if *record {
		recordHistories(t, id, "ro/happy/stage/prod", id+"/prod/canary/acme/us-east-1")
	}
}

func TestStartAfterCompletedRollout(t *testing.T) {
	id := start(t, minimal("again", "0s", false), "v1")
	st, _ := drive(t, id, time.Minute)
	require.Equal(t, workflows.Succeeded, st.State)
	assert.Equal(t, id, start(t, minimal("again", "0s", false), "v2"))
	st, _ = drive(t, id, time.Minute)
	assert.Equal(t, "v2", st.Version)
}

func TestStartWhileRunning(t *testing.T) {
	id := start(t, minimal("busy", "30s", false), "v1")
	_, _, err := rollouts.Start(context.Background(), minimal("busy", "30s", false), "v2")
	assert.True(t, errors.Is(err, cli.ErrConflict), "%v", err)
	require.NoError(t, rollouts.Abort(context.Background(), id))
	st, _ := drive(t, id, time.Minute)
	assert.Equal(t, workflows.Aborted, st.State)
}

func TestPipeliningQueues(t *testing.T) {
	v1 := start(t, minimal("pipe", "5s", true), "v1")
	waitFor(t, v1, 10*time.Second, func(s *workflows.RolloutStatus) bool {
		return s.Targets["staging/default/_/us-east-1"].State == workflows.StateBaking
	})
	v2 := start(t, minimal("pipe", "5s", true), "v2")
	assert.Equal(t, "ro/pipe/v1", v1)
	assert.Equal(t, "ro/pipe/v2", v2)
	st := waitFor(t, v2, 10*time.Second, func(s *workflows.RolloutStatus) bool { return s.State == workflows.StateWaitingForStage })
	assert.Equal(t, "staging", st.WaitingForStage)
	st1, _ := drive(t, v1, time.Minute)
	st2, _ := drive(t, v2, 2*time.Minute)
	assert.Equal(t, workflows.Succeeded, st1.State)
	assert.Equal(t, workflows.Succeeded, st2.State)
}

func TestMixedModeSharesStageLock(t *testing.T) {
	pipelined := start(t, minimal("mixed", "20s", true), "v1")
	waitFor(t, pipelined, 10*time.Second, func(s *workflows.RolloutStatus) bool {
		return s.Targets["staging/default/_/us-east-1"].State == workflows.StateBaking
	})
	plain := start(t, minimal("mixed", "20s", false), "v2")
	st := waitFor(t, plain, 10*time.Second, func(s *workflows.RolloutStatus) bool { return s.State == workflows.StateWaitingForStage })
	assert.Equal(t, "staging", st.WaitingForStage)
	require.NoError(t, rollouts.Abort(context.Background(), plain))
	require.NoError(t, rollouts.Abort(context.Background(), pipelined))
	drive(t, plain, time.Minute)
	drive(t, pipelined, time.Minute)
}

func TestFailingPauseResume(t *testing.T) {
	id := start(t, policy(t, "policy-failing.yaml", "flaky"), "v1")
	st, seen := drive(t, id, 3*time.Minute)
	assert.True(t, seen[workflows.StatePaused], "pre-prod pauses on the flaky validation")
	assert.Equal(t, workflows.Succeeded, st.State)
}

// recordHistories writes each workflow's history as JSON for replay tests.
func recordHistories(t *testing.T, ids ...string) {
	t.Helper()
	dir := "../../internal/workflows/testdata/histories"
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, id := range ids {
		var events []*historypb.HistoryEvent
		it := temporal.GetWorkflowHistory(context.Background(), id, "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		for it.HasNext() {
			e, err := it.Next()
			require.NoError(t, err)
			events = append(events, e)
		}
		b, err := protojson.Marshal(&historypb.History{Events: events})
		require.NoError(t, err)
		name := strings.ReplaceAll(id, "/", "_") + ".json"
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), b, 0o644))
	}
}

// failCustomer is an http-health stand-in that fails one customer.
type failCustomer string

func (f failCustomer) Validate(_ context.Context, r plugin.ValidateRequest) (plan.Verdict, error) {
	if r.Target.Customer == string(f) {
		return plan.Verdict{Outcome: plan.FAIL, Summary: "forced"}, nil
	}
	return plan.Verdict{Outcome: plan.PASS}, nil
}

// TestHeldStopsSiblings holds customer a while b and c are mid-rollout, so
// the stage sends stop to two running siblings. Its stage history backs
// the replay test for deterministic signal order.
func TestHeldStopsSiblings(t *testing.T) {
	const queue = "rollout-siblings"
	reg, err := registry.Load("../../plugins.yaml")
	require.NoError(t, err)
	builtins := builtin.Register()
	builtins["http-health"] = failCustomer("a")
	set, err := plugin.NewSet(reg, builtins, func(registry.Entry) (any, error) { return builtin.Register()["prometheus"], nil })
	require.NoError(t, err)
	w := worker.New(temporal, queue, worker.Options{})
	workflows.Register(w, &activities.Activities{Plugins: set})
	require.NoError(t, w.Start())
	defer w.Stop()

	p, diags := compiler.Compile([]byte(`apiVersion: rollout/v1
agent: siblings
ramper: { plugin: mock-ramper }
validations:
  health: { plugin: http-health, params: { path: /h } }
stages:
  - name: prod
    regions: [us-east-1]
    steps: [25, 50, 100]
    bake: 2s
    validate: [health]
    on_failure: [hold]
    waves:
      - { name: w, customers: [a, b, c], max_parallel: 3 }
`), reg, "v1")
	require.Empty(t, diags)
	run, err := temporal.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{ID: p.ID, TaskQueue: queue}, workflows.GlobalRollout, *p)
	require.NoError(t, err)
	var st workflows.RolloutStatus
	require.NoError(t, run.Get(context.Background(), &st))
	assert.Equal(t, workflows.Held, st.State)
	for _, c := range []string{"b", "c"} {
		ts := st.Targets["prod/w/"+c+"/us-east-1"]
		assert.Equal(t, workflows.StateHeld, ts.State, c)
		assert.Contains(t, ts.Message, "sibling", c)
	}
	if *record {
		recordHistories(t, "ro/siblings/stage/prod")
	}
}
