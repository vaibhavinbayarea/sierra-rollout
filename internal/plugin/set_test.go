package plugin_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sierra-rollout/internal/plugin"
	"sierra-rollout/internal/plugin/builtin"
	"sierra-rollout/internal/registry"
)

func loadRegistry(t *testing.T, extra string) *registry.Registry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugins.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`plugins:
  - { name: prometheus,      kind: validator, builtin: true }
  - { name: manual-approval, kind: validator, builtin: true }
  - { name: mock-ramper,     kind: ramper,    builtin: true }
  - { name: log-notifier,    kind: notifier,  builtin: true }
`+extra), 0o644))
	r, err := registry.Load(path)
	require.NoError(t, err)
	return r
}

func TestSetResolves(t *testing.T) {
	s, err := plugin.NewSet(loadRegistry(t, ""), builtin.Register(), nil)
	require.NoError(t, err)

	_, err = s.Ramper("mock-ramper")
	assert.NoError(t, err)
	_, err = s.Validator("prometheus")
	assert.NoError(t, err)
	_, err = s.Notifier("log-notifier")
	assert.NoError(t, err)

	_, err = s.Validator("log-notifier")
	assert.True(t, errors.Is(err, plugin.ErrPluginMissing), "wrong kind: %v", err)
	_, err = s.Validator("http-health") // builtin exists but not registered
	assert.True(t, errors.Is(err, plugin.ErrPluginMissing))
}

func TestSetRemoteWithoutFactory(t *testing.T) {
	_, err := plugin.NewSet(loadRegistry(t, "  - { name: team/x, kind: validator, endpoint: h:1 }\n"), builtin.Register(), nil)
	assert.Error(t, err)
}

func TestSetRemoteFactory(t *testing.T) {
	var built []string
	remote := func(e registry.Entry) (any, error) {
		built = append(built, e.Name)
		return builtin.Register()["prometheus"], nil
	}
	s, err := plugin.NewSet(loadRegistry(t, "  - { name: team/x, kind: validator, endpoint: h:1 }\n"), builtin.Register(), remote)
	require.NoError(t, err)
	_, err = s.Validator("team/x")
	assert.NoError(t, err)
	assert.Equal(t, []string{"team/x"}, built)
}
