package spec

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return b
}

// lineOf returns the 1-based line of the first line containing substr.
func lineOf(t *testing.T, data []byte, substr string) int {
	t.Helper()
	for i, l := range strings.Split(string(data), "\n") {
		if strings.Contains(l, substr) {
			return i + 1
		}
	}
	t.Fatalf("substring %q not found", substr)
	return 0
}

func TestParseFullExample(t *testing.T) {
	p, _, diags := Parse(readFixture(t, "full.yaml"))
	require.Empty(t, diags)
	assert.Equal(t, "support-agent", p.Agent)
	require.Len(t, p.Stages, 3)
	require.NotNil(t, p.Stages[2].Waves[1].MaxParallel)
	assert.Equal(t, 2, *p.Stages[2].Waves[1].MaxParallel)
	assert.Equal(t, []int{25, 100}, p.Stages[2].Waves[1].Steps)
	assert.Equal(t, 5*time.Minute, p.Defaults.Bake.D())
	assert.Equal(t, "log-notifier", p.Defaults.OnFailure[0].Notify)
	assert.Equal(t, 24*time.Hour, p.Defaults.OnFailure[1].Pause.Timeout.D())
	assert.Equal(t, "final_step", p.Validations["signoff"].When)
}

func TestParseHoldScalar(t *testing.T) {
	for _, src := range []string{"on_failure: [hold]", "on_failure: [{hold: true}]"} {
		p, _, diags := Parse([]byte("apiVersion: rollout/v1\ndefaults:\n  " + src + "\n"))
		require.Empty(t, diags, src)
		assert.Equal(t, []FailureAction{{Hold: true}}, p.Defaults.OnFailure, src)
	}
}

func TestParseUnknownField(t *testing.T) {
	src := []byte("apiVersion: rollout/v1\nstages:\n  - name: a\n    regoins: [x]\n")
	_, _, diags := Parse(src)
	require.Len(t, diags, 1)
	assert.Contains(t, diags[0].Message, "regoins")
	assert.Equal(t, 4, diags[0].Line)
}

func TestParseBadDuration(t *testing.T) {
	src := []byte("apiVersion: rollout/v1\ndefaults:\n  bake: 5 minutes\n")
	_, _, diags := Parse(src)
	require.Len(t, diags, 1)
	assert.Equal(t, "defaults.bake", diags[0].Path)
	assert.Equal(t, 3, diags[0].Line)
}

func TestLinesLookup(t *testing.T) {
	data := readFixture(t, "full.yaml")
	_, lines, diags := Parse(data)
	require.Empty(t, diags)
	assert.Equal(t, lineOf(t, data, "steps: [25, 100]"), lines.Line("stages[2].waves[1].steps"))
	assert.Equal(t, lineOf(t, data, "health:     {"), lines.Line("validations.health"))
	assert.Equal(t, 0, lines.Line("nope.nothing"))
}

func TestParseMalformedYAML(t *testing.T) {
	p, _, diags := Parse([]byte("apiVersion: rollout/v1\nstages: [\n  { name: a\n"))
	assert.Nil(t, p)
	require.Len(t, diags, 1)
	assert.Greater(t, diags[0].Line, 0)
}
