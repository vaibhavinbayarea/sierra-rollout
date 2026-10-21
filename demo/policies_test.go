// Package demo holds the demo policies; this test keeps them compiling.
package demo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sierra-rollout/internal/compiler"
	"sierra-rollout/internal/registry"
)

func TestDemoPoliciesCompile(t *testing.T) {
	reg, err := registry.Load("plugins.yaml")
	require.NoError(t, err)
	files, err := filepath.Glob("policies/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		if strings.HasSuffix(f, "broken.yaml") {
			continue
		}
		t.Run(filepath.Base(f), func(t *testing.T) {
			src, err := os.ReadFile(f)
			require.NoError(t, err)
			p, diags := compiler.Compile(src, reg, "v1")
			require.Empty(t, diags)
			assert.Len(t, p.Targets(), 8)
		})
	}
}

func TestBrokenPolicyDiagnostics(t *testing.T) {
	reg, err := registry.Load("plugins.yaml")
	require.NoError(t, err)
	src, err := os.ReadFile("policies/broken.yaml")
	require.NoError(t, err)
	_, diags := compiler.Compile(src, reg, "v1")
	var msgs []string
	for _, d := range diags {
		msgs = append(msgs, d.Message)
	}
	all := strings.Join(msgs, "\n")
	for _, want := range []string{`invalid name "Agent_Broken"`, `unknown plugin "datadog"`, "path", "must end at 100", `undefined validation "smoke"`} {
		assert.Contains(t, all, want)
	}
}
