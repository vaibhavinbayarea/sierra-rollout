package api

import (
	"embed"
	"net/http"
	"sort"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
)

// RolloutSummary is one entry of GET /v1/rollouts.
type RolloutSummary struct {
	ID        string    `json:"id"`
	StartTime time.Time `json:"start_time"`
	Running   bool      `json:"running"`
}

// listRollouts returns the most recent run of each rollout, newest first.
func (s *server) listRollouts(w http.ResponseWriter, r *http.Request) {
	resp, err := s.temporal.ListWorkflow(r.Context(), &workflowservice.ListWorkflowExecutionsRequest{
		PageSize: 100,
		Query:    "WorkflowType = 'GlobalRollout'",
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	latest := map[string]RolloutSummary{}
	for _, e := range resp.Executions {
		sum := RolloutSummary{
			ID:        e.GetExecution().GetWorkflowId(),
			StartTime: e.GetStartTime().AsTime(),
			Running:   e.GetStatus() == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
		}
		if cur, ok := latest[sum.ID]; !ok || sum.StartTime.After(cur.StartTime) {
			latest[sum.ID] = sum
		}
	}
	out := make([]RolloutSummary, 0, len(latest))
	for _, sum := range latest {
		out = append(out, sum)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartTime.After(out[j].StartTime) })
	writeJSON(w, http.StatusOK, out)
}

//go:embed ui/index.html
var uiFS embed.FS

// ui serves the demo dashboard.
func (s *server) ui(w http.ResponseWriter, _ *http.Request) {
	b, _ := uiFS.ReadFile("ui/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}
