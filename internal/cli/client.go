// Package cli implements rolloutctl: an HTTP client for the rollout API,
// renderers, and cobra commands.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"sierra-rollout/internal/api"
	"sierra-rollout/internal/plan"
	"sierra-rollout/internal/spec"
	"sierra-rollout/internal/workflows"
)

var (
	ErrConflict = errors.New("conflict")
	ErrNotFound = errors.New("not found")
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// Plan compiles a policy. Diagnostics are returned with a nil error.
func (c *Client) Plan(ctx context.Context, policy []byte, version string) (*plan.Plan, []spec.Diagnostic, error) {
	var p plan.Plan
	diags, err := c.compile(ctx, "/v1/plans", policy, version, &p)
	if err != nil || len(diags) > 0 {
		return nil, diags, err
	}
	return &p, nil, nil
}

// Start compiles and starts a rollout, returning its ID.
func (c *Client) Start(ctx context.Context, policy []byte, version string) (string, []spec.Diagnostic, error) {
	var resp api.StartResponse
	diags, err := c.compile(ctx, "/v1/rollouts", policy, version, &resp)
	return resp.ID, diags, err
}

func (c *Client) Status(ctx context.Context, id string) (*workflows.RolloutStatus, error) {
	var st workflows.RolloutStatus
	if err := c.do(ctx, http.MethodGet, "/v1/rollouts/"+url.PathEscape(id), nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (c *Client) Signal(ctx context.Context, id, tid, signal, validation string) error {
	path := "/v1/rollouts/" + url.PathEscape(id) + "/targets/" + url.PathEscape(tid) + "/signal"
	return c.do(ctx, http.MethodPost, path, api.SignalRequest{Signal: signal, Validation: validation}, nil)
}

func (c *Client) Abort(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/rollouts/"+url.PathEscape(id)+"/abort", nil, nil)
}

func (c *Client) compile(ctx context.Context, path string, policy []byte, version string, out any) ([]spec.Diagnostic, error) {
	err := c.do(ctx, http.MethodPost, path, api.PlanRequest{Policy: string(policy), Version: version}, out)
	var de *diagError
	if errors.As(err, &de) {
		return de.diags, nil
	}
	return nil, err
}

type diagError struct{ diags []spec.Diagnostic }

func (e *diagError) Error() string { return fmt.Sprintf("%d policy errors", len(e.diags)) }

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 300 {
		if out == nil {
			return nil
		}
		return json.Unmarshal(data, out)
	}
	if resp.StatusCode == http.StatusUnprocessableEntity {
		var d api.DiagnosticsResponse
		if err := json.Unmarshal(data, &d); err == nil {
			return &diagError{diags: d.Errors}
		}
	}
	var e api.ErrorResponse
	msg := string(data)
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	switch resp.StatusCode {
	case http.StatusConflict:
		return fmt.Errorf("%w: %s", ErrConflict, msg)
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, msg)
	}
	return fmt.Errorf("server returned %d: %s", resp.StatusCode, msg)
}
