package builtin

import (
	"context"
	"log/slog"
	"sync"

	"sierra-rollout/internal/plugin"
)

// MockRamper records live and shadow traffic state in memory.
type MockRamper struct {
	mu      sync.Mutex
	state   map[stateKey]plugin.RampResult
	history []plugin.RampRequest
}

type stateKey struct {
	target plugin.Target
	mode   string
}

func NewMockRamper() *MockRamper {
	return &MockRamper{state: map[stateKey]plugin.RampResult{}}
}

func mode(m string) string {
	if m == "" {
		return plugin.ModeLive
	}
	return m
}

func (m *MockRamper) SetPercent(_ context.Context, r plugin.RampRequest) (plugin.RampResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := plugin.RampResult{Percent: r.Percent, Version: r.Version}
	m.state[stateKey{r.Target, mode(r.Mode)}] = res
	m.history = append(m.history, r)
	slog.Info("mock ramp", "target", r.Target, "version", r.Version, "percent", r.Percent, "mode", mode(r.Mode))
	return res, nil
}

func (m *MockRamper) Current(_ context.Context, t plugin.Target, md string) (int, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.state[stateKey{t, mode(md)}]
	return s.Percent, s.Version, nil
}

// History returns every SetPercent request, oldest first.
func (m *MockRamper) History() []plugin.RampRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]plugin.RampRequest(nil), m.history...)
}
