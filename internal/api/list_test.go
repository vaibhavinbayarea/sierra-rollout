package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func exec(id string, start time.Time, status enumspb.WorkflowExecutionStatus) *workflowpb.WorkflowExecutionInfo {
	return &workflowpb.WorkflowExecutionInfo{
		Execution: &commonpb.WorkflowExecution{WorkflowId: id},
		StartTime: timestamppb.New(start),
		Status:    status,
	}
}

func TestListRolloutsDedupesNewestFirst(t *testing.T) {
	c, h := setup(t)
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	var req *workflowservice.ListWorkflowExecutionsRequest
	c.On("ListWorkflow", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) { req = args.Get(1).(*workflowservice.ListWorkflowExecutionsRequest) }).
		Return(&workflowservice.ListWorkflowExecutionsResponse{Executions: []*workflowpb.WorkflowExecutionInfo{
			exec("ro/b", t0.Add(2*time.Minute), enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING),
			exec("ro/a", t0.Add(time.Minute), enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED),
			exec("ro/b", t0, enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED), // older run of ro/b
		}}, nil)

	rec := do(t, h, "GET", "/v1/rollouts", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got []RolloutSummary
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 2)
	assert.Equal(t, "ro/b", got[0].ID)
	assert.True(t, got[0].Running)
	assert.Equal(t, "ro/a", got[1].ID)
	assert.False(t, got[1].Running)
	assert.Contains(t, req.Query, "WorkflowType = 'GlobalRollout'")
}

func TestUIServed(t *testing.T) {
	_, h := setup(t)
	rec := do(t, h, "GET", "/ui", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, rec.Body.String(), "/v1/rollouts")
}
