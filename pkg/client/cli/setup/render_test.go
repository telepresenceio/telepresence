package setup

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
)

func renderSummary() *Summary {
	return &Summary{
		Facts:   recFacts(),
		Answers: recAnswers(),
		Proposal: &Proposal{
			Action: ActionInstall,
			Values: map[string]any{
				"agentInjector": map[string]any{"enabled": false},
				"nodeAgent":     map[string]any{"enabled": true},
			},
			Notes: []Note{
				{Level: NoteInfo, Text: "an informational note"},
				{Level: NoteWarning, Text: "a cautionary note"},
			},
		},
		Action: ActionWord(ActionInstall, false),
	}
}

func TestPrintReport_Text(t *testing.T) {
	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)

	require.NoError(t, PrintReport(cmd, renderSummary()))
	text := out.String()
	assert.Contains(t, text, "Findings:")
	assert.Contains(t, text, "  privileges: cluster-wide install yes")
	assert.Contains(t, text, "Proposed configuration:")
	assert.Contains(t, text, "  nodeAgent:")
	assert.Contains(t, text, "Notes:")
	assert.Contains(t, text, "  note: an informational note")
	assert.Contains(t, text, "  warning: a cautionary note")
	assert.Contains(t, text, "Action: would-install")
}

func TestActionWord(t *testing.T) {
	assert.Equal(t, "would-install", ActionWord(ActionInstall, false))
	assert.Equal(t, "would-upgrade", ActionWord(ActionUpgrade, false))
	assert.Equal(t, "install", ActionWord(ActionInstall, true))
	assert.Equal(t, "none", ActionWord(ActionNone, false))
	assert.Equal(t, "none", ActionWord(ActionNone, true))
}

// TestPrintReport_LocalOutputFlagDoesNotConfuseFormatting pins the deliberate
// shadowing of the deprecated hidden global --output format flag by setup's
// local --output FILE flag: the output package recognizes the global flag by
// its "default" default value, so a set local flag with default "" must not
// switch the report into formatted mode.
func TestPrintReport_LocalOutputFlagDoesNotConfuseFormatting(t *testing.T) {
	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.Flags().String("output", "", "")
	require.NoError(t, cmd.Flags().Set("output", "/tmp/values.yaml"))

	assert.False(t, output.WantsFormatted(cmd))
	require.NoError(t, PrintReport(cmd, renderSummary()))
	assert.Contains(t, out.String(), "Findings:")
}

func TestSummary_MarshalsCleanJSON(t *testing.T) {
	data, err := json.Marshal(renderSummary())
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	assert.Contains(t, m, "facts")
	assert.Contains(t, m, "answers")
	assert.Contains(t, m, "proposal")
	assert.Equal(t, "would-install", m["action"])
}

func TestWriteValues_RoundTrip(t *testing.T) {
	values := map[string]any{
		"nodeAgent": map[string]any{"enabled": true},
		"client":    map[string]any{"cluster": map[string]any{"mappedNamespaces": []any{"foo"}}},
	}
	buf := &bytes.Buffer{}
	require.NoError(t, WriteValues(buf, values))

	var got map[string]any
	require.NoError(t, yaml.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, values, got)
}

func TestWriteValuesFile_RoundTrip(t *testing.T) {
	values := map[string]any{
		"nodeAgent":  map[string]any{"enabled": true},
		"namespaces": []any{"foo", "ambassador"},
	}
	path := filepath.Join(t.TempDir(), "values.yaml")
	require.NoError(t, WriteValuesFile(path, values))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(data, &got))
	assert.Equal(t, values, got)
}
