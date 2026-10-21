package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/mocks"

	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/registry"
	"sierra-rollout/internal/spec"
	"sierra-rollout/internal/workflows"
)

// jsonValue is a converter.EncodedValue backed by a Go value.
type jsonValue struct{ v any }

func (j jsonValue) HasValue() bool { return j.v != nil }
func (j jsonValue) Get(ptr any) error {
	b, err := json.Marshal(j.v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, ptr)
}

func setup(t *testing.T) (*mocks.Client, http.Handler) {
	t.Helper()
	reg, err := registry.Load("../compiler/testdata/plugins.yaml")
	require.NoError(t, err)
	c := &mocks.Client{}
	t.Cleanup(func() { c.AssertExpectations(t) })
	return c, NewServer(c, reg, TaskQueue)
}

func fullPolicy(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../compiler/testdata/full.yaml")
	require.NoError(t, err)
	return string(b)
}

func do(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func statusWith(id, state string) workflows.RolloutStatus {
	return workflows.RolloutStatus{ID: "ro/support-agent", State: workflows.StateRunning,
		Targets: map[string]workflows.TargetStatus{id: {ID: id, State: state, AwaitingApproval: []string{"signoff"}}}}
}

func TestPlanReturnsPlan(t *testing.T) {
	_, h := setup(t)
	rec := do(t, h, "POST", "/v1/plans", PlanRequest{Policy: fullPolicy(t), Version: "v1"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var p plan.Plan
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	assert.Len(t, p.Targets(), 8)
}

func TestPlanDiagnostics(t *testing.T) {
	_, h := setup(t)
	rec := do(t, h, "POST", "/v1/plans", PlanRequest{Policy: "apiVersion: rollout/v1\nagent: a\nstages:\n  - name: s\n    regoins: [x]\n", Version: "v1"})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	var body struct{ Errors []spec.Diagnostic }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotEmpty(t, body.Errors)
	assert.Greater(t, body.Errors[0].Line, 0)
}

func TestPlanRequiresVersion(t *testing.T) {
	_, h := setup(t)
	rec := do(t, h, "POST", "/v1/plans", PlanRequest{Policy: fullPolicy(t)})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestStartConflict(t *testing.T) {
	c, h := setup(t)
	c.On("ExecuteWorkflow", mock.Anything, mock.Anything, "GlobalRollout", mock.Anything).
		Return(nil, serviceerror.NewWorkflowExecutionAlreadyStarted("running", "", "run-1"))
	rec := do(t, h, "POST", "/v1/rollouts", PlanRequest{Policy: fullPolicy(t), Version: "v1"})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "ro/support-agent already running")
}

func TestStartAfterCompletedRollout(t *testing.T) {
	c, h := setup(t)
	run := &mocks.WorkflowRun{}
	run.On("GetID").Return("ro/support-agent")
	var opts client.StartWorkflowOptions
	c.On("ExecuteWorkflow", mock.Anything, mock.Anything, "GlobalRollout", mock.Anything).
		Run(func(args mock.Arguments) { opts = args.Get(1).(client.StartWorkflowOptions) }).
		Return(run, nil)
	rec := do(t, h, "POST", "/v1/rollouts", PlanRequest{Policy: fullPolicy(t), Version: "v1"})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"id":"ro/support-agent"}`, rec.Body.String())
	assert.Equal(t, "ro/support-agent", opts.ID)
	assert.Equal(t, TaskQueue, opts.TaskQueue)
	assert.Equal(t, enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE, opts.WorkflowIDReusePolicy)
	assert.Equal(t, enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL, opts.WorkflowIDConflictPolicy)
	assert.True(t, opts.WorkflowExecutionErrorWhenAlreadyStarted)
}

func TestSignalRouteWithSlashIDs(t *testing.T) {
	c, h := setup(t)
	tid := "prod/canary/acme/us-east-1"
	c.On("QueryWorkflow", mock.Anything, "ro/support-agent", "", workflows.QueryStatus).
		Return(jsonValue{statusWith(tid, workflows.StatePaused)}, nil)
	c.On("SignalWorkflow", mock.Anything, "ro/support-agent/"+tid, "", workflows.SignalResume, workflows.OperatorSignal{}).Return(nil)
	rec := do(t, h, "POST", "/v1/rollouts/ro%2Fsupport-agent/targets/prod%2Fcanary%2Facme%2Fus-east-1/signal", SignalRequest{Signal: "resume"})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestSignalApproveValidation(t *testing.T) {
	c, h := setup(t)
	tid := "prod/canary/acme/us-east-1"
	c.On("QueryWorkflow", mock.Anything, "ro/support-agent", "", workflows.QueryStatus).
		Return(jsonValue{statusWith(tid, workflows.StateAwaitingApproval)}, nil)
	c.On("SignalWorkflow", mock.Anything, "ro/support-agent/"+tid, "", workflows.SignalApprove, workflows.OperatorSignal{Validation: "signoff"}).Return(nil)
	rec := do(t, h, "POST", "/v1/rollouts/ro%2Fsupport-agent/targets/prod%2Fcanary%2Facme%2Fus-east-1/signal", SignalRequest{Signal: "approve", Validation: "signoff"})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = do(t, h, "POST", "/v1/rollouts/ro%2Fsupport-agent/targets/prod%2Fcanary%2Facme%2Fus-east-1/signal", SignalRequest{Signal: "approve", Validation: "health"})
	assert.Equal(t, http.StatusConflict, rec.Code, "health is not awaiting approval")
}

func TestSignalNotApplicable(t *testing.T) {
	c, h := setup(t)
	tid := "prod/canary/acme/us-east-1"
	c.On("QueryWorkflow", mock.Anything, "ro/support-agent", "", workflows.QueryStatus).
		Return(jsonValue{statusWith(tid, workflows.StateBaking)}, nil)
	path := "/v1/rollouts/ro%2Fsupport-agent/targets/prod%2Fcanary%2Facme%2Fus-east-1/signal"
	assert.Equal(t, http.StatusConflict, do(t, h, "POST", path, SignalRequest{Signal: "resume"}).Code)
	assert.Equal(t, http.StatusNotFound, do(t, h, "POST", "/v1/rollouts/ro%2Fsupport-agent/targets/nope/signal", SignalRequest{Signal: "resume"}).Code)
	assert.Equal(t, http.StatusBadRequest, do(t, h, "POST", path, SignalRequest{Signal: "dance"}).Code)
}

func TestStatus(t *testing.T) {
	c, h := setup(t)
	c.On("QueryWorkflow", mock.Anything, "ro/support-agent", "", workflows.QueryStatus).
		Return(jsonValue{statusWith("x", workflows.StateBaking)}, nil)
	rec := do(t, h, "GET", "/v1/rollouts/ro%2Fsupport-agent", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var st workflows.RolloutStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
	assert.Equal(t, workflows.StateBaking, st.Targets["x"].State)
}

func TestStatusNotFound(t *testing.T) {
	c, h := setup(t)
	c.On("QueryWorkflow", mock.Anything, "ro/nope", "", workflows.QueryStatus).Return(nil, serviceerror.NewNotFound("no such workflow"))
	assert.Equal(t, http.StatusNotFound, do(t, h, "GET", "/v1/rollouts/ro%2Fnope", nil).Code)
}

func TestAbort(t *testing.T) {
	c, h := setup(t)
	c.On("SignalWorkflow", mock.Anything, "ro/support-agent", "", workflows.SignalAbort, nil).Return(nil)
	assert.Equal(t, http.StatusOK, do(t, h, "POST", "/v1/rollouts/ro%2Fsupport-agent/abort", nil).Code)
}
