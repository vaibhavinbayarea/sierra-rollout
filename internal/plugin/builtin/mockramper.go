package builtin

import (
	"context"
	"log/slog"
	"sync"

	"sierra-rollout/internal/plugin"
)

// MockRamper records traffic state in memory.
type MockRamper struct {
	mu      sync.Mutex
	state   map[plugin.Target]plugin.RampResult
	history []plugin.RampRequest
}

func NewMockRamper() *MockRamper {
	return &MockRamper{state: map[plugin.Target]plugin.RampResult{}}
}

func (m *MockRamper) SetPercent(_ context.Context, r plugin.RampRequest) (plugin.RampResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := plugin.RampResult{Percent: r.Percent, Version: r.Version}
	m.state[r.Target] = res
	m.history = append(m.history, r)
	slog.Info("mock ramp", "target", r.Target, "version", r.Version, "percent", r.Percent)
	return res, nil
}

func (m *MockRamper) Current(_ context.Context, t plugin.Target) (int, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.state[t]
	return s.Percent, s.Version, nil
}

// History returns every SetPercent request, oldest first.
func (m *MockRamper) History() []plugin.RampRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]plugin.RampRequest(nil), m.history...)
}
