package registry

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const builtins = `
  - { name: prometheus,      kind: validator, builtin: true }
  - { name: http-health,     kind: validator, builtin: true }
  - { name: manual-approval, kind: validator, builtin: true }
  - { name: mock-ramper,     kind: ramper,    builtin: true }
  - { name: log-notifier,    kind: notifier,  builtin: true }
`

func writeRegistry(t *testing.T, entries string) string {
	t.Helper()
	dir := t.TempDir()
	csat, err := os.ReadFile("testdata/csat.json")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "csat.json"), csat, 0o644))
	path := filepath.Join(dir, "plugins.yaml")
	require.NoError(t, os.WriteFile(path, []byte("plugins:\n"+entries), 0o644))
	return path
}

func TestLoadBuiltinsAndRemote(t *testing.T) {
	r, err := Load(writeRegistry(t, builtins+
		"  - { name: eval-team/csat, kind: validator, endpoint: localhost:9000, timeout: 2m, params_schema: csat.json }\n"))
	require.NoError(t, err)

	csat, ok := r.Get("eval-team/csat")
	require.True(t, ok)
	assert.Equal(t, 2*time.Minute, csat.Timeout)
	assert.False(t, csat.Builtin)
	assert.Equal(t, KindValidator, csat.Kind)

	prom, ok := r.Get("prometheus")
	require.True(t, ok)
	assert.NotEmpty(t, prom.ParamsSchema)
	assert.True(t, prom.Builtin)
}

func TestLoadRejects(t *testing.T) {
	cases := map[string]string{
		"unknown kind":     "  - { name: x, kind: gizmo, endpoint: h:1 }\n",
		"duplicate name":   builtins + "  - { name: prometheus, kind: validator, builtin: true }\n",
		"unknown builtin":  "  - { name: datadog, kind: validator, builtin: true }\n",
		"missing endpoint": "  - { name: team/x, kind: validator }\n",
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeRegistry(t, entries))
			require.Error(t, err)
			assert.Regexp(t, `x|prometheus|datadog|team/x`, err.Error())
		})
	}
}

func TestValidateParams(t *testing.T) {
	r, err := Load(writeRegistry(t, builtins))
	require.NoError(t, err)

	assert.NoError(t, r.ValidateParams("prometheus", map[string]any{"query": "x", "max": 0.01}))
	assert.NoError(t, r.ValidateParams("prometheus", map[string]any{"query": "x", "stub": map[string]any{"outcome": "FAIL", "after_attempt": 2}}))
	assert.Error(t, r.ValidateParams("prometheus", map[string]any{"max": 0.01}), "missing query")
	assert.Error(t, r.ValidateParams("prometheus", map[string]any{"query": "x", "foo": 1}), "extra key")
	assert.Error(t, r.ValidateParams("prometheus", map[string]any{"query": "x", "stub": map[string]any{"outcome": "MAYBE"}}), "bad outcome")
	assert.NoError(t, r.ValidateParams("log-notifier", nil))
	assert.Error(t, r.ValidateParams("nope", nil))
}

func TestLoadModes(t *testing.T) {
	r, err := Load(writeRegistry(t, builtins+"  - { name: team/r, kind: ramper, endpoint: h:1, modes: [live, shadow] }\n  - { name: team/l, kind: ramper, endpoint: h:1 }\n"))
	require.NoError(t, err)
	mock, _ := r.Get("mock-ramper")
	assert.Equal(t, []string{"live", "shadow"}, mock.Modes)
	tr, _ := r.Get("team/r")
	assert.Equal(t, []string{"live", "shadow"}, tr.Modes)
	tl, _ := r.Get("team/l")
	assert.Equal(t, []string{"live"}, tl.Modes)

	_, err = Load(writeRegistry(t, "  - { name: team/x, kind: ramper, endpoint: h:1, modes: [sideways] }\n"))
	assert.ErrorContains(t, err, "sideways")
}
