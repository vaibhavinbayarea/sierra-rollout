package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"sierra-rollout/internal/compiler"
	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/spec"
	"sierra-rollout/internal/workflows"
)

type PlanRequest struct {
	Policy  string `json:"policy"`
	Version string `json:"version"`
}

type StartResponse struct {
	ID string `json:"id"`
}

type SignalRequest struct {
	Signal     string `json:"signal"`
	Validation string `json:"validation,omitempty"`
}

type DiagnosticsResponse struct {
	Errors []spec.Diagnostic `json:"errors"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

// signalStates lists the target state each operator signal requires.
var signalStates = map[string]string{
	workflows.SignalApprove: workflows.StateAwaitingApproval,
	workflows.SignalReject:  workflows.StateAwaitingApproval,
	workflows.SignalResume:  workflows.StatePaused,
	workflows.SignalRetry:   workflows.StatePaused,
	workflows.SignalAbort:   workflows.StatePaused,
}

func (s *server) plan(w http.ResponseWriter, r *http.Request) {
	p, ok := s.compile(w, r)
	if ok {
		writeJSON(w, http.StatusOK, p)
	}
}

func (s *server) start(w http.ResponseWriter, r *http.Request) {
	p, ok := s.compile(w, r)
	if !ok {
		return
	}
	run, err := s.temporal.ExecuteWorkflow(r.Context(), client.StartWorkflowOptions{
		ID:                                       p.ID,
		TaskQueue:                                s.taskQueue,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		WorkflowIDConflictPolicy:                 enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}, "GlobalRollout", *p)
	var started *serviceerror.WorkflowExecutionAlreadyStarted
	switch {
	case errors.As(err, &started):
		writeError(w, http.StatusConflict, fmt.Sprintf("rollout %s already running", p.ID))
	case err != nil:
		writeError(w, http.StatusBadGateway, err.Error())
	default:
		writeJSON(w, http.StatusCreated, StartResponse{ID: run.GetID()})
	}
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	st, ok := s.query(w, r.Context(), r.PathValue("id"))
	if ok {
		writeJSON(w, http.StatusOK, st)
	}
}

func (s *server) signal(w http.ResponseWriter, r *http.Request) {
	id, tid := r.PathValue("id"), r.PathValue("tid")
	var req SignalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	want, known := signalStates[req.Signal]
	if !known {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown signal %q", req.Signal))
		return
	}
	st, ok := s.query(w, r.Context(), id)
	if !ok {
		return
	}
	ts, found := st.Targets[tid]
	if !found {
		writeError(w, http.StatusNotFound, fmt.Sprintf("target %q not found in %s", tid, id))
		return
	}
	if ts.State != want {
		writeError(w, http.StatusConflict, fmt.Sprintf("%s requires target state %s, target is %s", req.Signal, want, ts.State))
		return
	}
	if req.Validation != "" && !slices.Contains(ts.AwaitingApproval, req.Validation) {
		writeError(w, http.StatusConflict, fmt.Sprintf("validation %q is not awaiting approval", req.Validation))
		return
	}
	err := s.temporal.SignalWorkflow(r.Context(), id+"/"+tid, "", req.Signal, workflows.OperatorSignal{Validation: req.Validation})
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

func (s *server) abort(w http.ResponseWriter, r *http.Request) {
	err := s.temporal.SignalWorkflow(r.Context(), r.PathValue("id"), "", workflows.SignalAbort, nil)
	var nf *serviceerror.NotFound
	switch {
	case errors.As(err, &nf):
		writeError(w, http.StatusNotFound, err.Error())
	case err != nil:
		writeError(w, http.StatusBadGateway, err.Error())
	default:
		writeJSON(w, http.StatusOK, struct{}{})
	}
}

func (s *server) compile(w http.ResponseWriter, r *http.Request) (*plan.Plan, bool) {
	var req PlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return nil, false
	}
	if req.Version == "" {
		writeError(w, http.StatusBadRequest, "version is required")
		return nil, false
	}
	p, diags := compiler.Compile([]byte(req.Policy), s.reg, req.Version)
	if len(diags) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, DiagnosticsResponse{Errors: diags})
		return nil, false
	}
	return p, true
}

func (s *server) query(w http.ResponseWriter, ctx context.Context, id string) (workflows.RolloutStatus, bool) {
	var st workflows.RolloutStatus
	v, err := s.temporal.QueryWorkflow(ctx, id, "", workflows.QueryStatus)
	if err == nil {
		err = v.Get(&st)
	}
	var nf *serviceerror.NotFound
	switch {
	case errors.As(err, &nf):
		writeError(w, http.StatusNotFound, fmt.Sprintf("rollout %s not found", id))
	case err != nil:
		writeError(w, http.StatusBadGateway, err.Error())
	default:
		return st, true
	}
	return st, false
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, ErrorResponse{Error: msg})
}
