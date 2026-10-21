package compiler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sierra-rollout/internal/registry"
)

// TestExamplesCompile keeps examples/ valid against the shipped registry.
func TestExamplesCompile(t *testing.T) {
	reg, err := registry.Load("../../plugins.yaml")
	require.NoError(t, err)
	files, err := filepath.Glob("../../examples/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			src, err := os.ReadFile(f)
			require.NoError(t, err)
			p, diags := Compile(src, reg, "v1")
			assert.Empty(t, diags)
			assert.NotNil(t, p)
		})
	}
}
