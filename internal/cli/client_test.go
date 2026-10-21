package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientEscapesIDs(t *testing.T) {
	var rawPath, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawPath = r.URL.RawPath
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		body = string(b)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	require.NoError(t, c.Signal(context.Background(), "ro/a", "prod/w/c/r", "resume", ""))
	assert.Equal(t, "/v1/rollouts/ro%2Fa/targets/prod%2Fw%2Fc%2Fr/signal", rawPath)
	assert.JSONEq(t, `{"signal":"resume"}`, body)
}

func TestClientMapsErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/rollouts":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"rollout ro/a already running"}`))
		case "/v1/plans":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"errors":[{"path":"stages[0].steps","line":7,"message":"steps must end at 100"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"rollout ro/x not found"}`))
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	ctx := context.Background()

	_, _, err := c.Start(ctx, []byte("x"), "v1")
	assert.True(t, errors.Is(err, ErrConflict), "%v", err)
	assert.ErrorContains(t, err, "already running")

	p, diags, err := c.Plan(ctx, []byte("x"), "v1")
	require.NoError(t, err)
	assert.Nil(t, p)
	require.Len(t, diags, 1)
	assert.Equal(t, 7, diags[0].Line)

	_, err = c.Status(ctx, "ro/x")
	assert.True(t, errors.Is(err, ErrNotFound), "%v", err)
}
