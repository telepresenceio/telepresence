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
		Action: ActionWord(ActionInstall, true),
	}
}

func TestPrintReport_Text(t *testing.T) {
	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)

	require.NoError(t, PrintReport(cmd, renderSummary(), true))
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

func TestPrintReport_MappedManifestSection(t *testing.T) {
	s := renderSummary()
	s.Proposal.MappedNamespaces = []string{"foo"}

	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	require.NoError(t, PrintReport(cmd, s, true))
	assert.Contains(t, out.String(), "Workstation state manifest:")
	assert.Contains(t, out.String(), "kind: WorkstationState")

	out.Reset()
	require.NoError(t, PrintReport(cmd, s, false))
	assert.NotContains(t, out.String(), "Workstation state manifest:")
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

func TestWorkstationStateSnippet(t *testing.T) {
	data, err := WorkstationStateSnippet("ambassador", []string{"foo", "bar"})
	require.NoError(t, err)
	text := string(data)
	assert.Contains(t, text, "apiVersion: telepresence.io/v1alpha1")
	assert.Contains(t, text, "kind: WorkstationState")
	assert.Contains(t, text, "managerNamespace: ambassador")
	assert.Contains(t, text, "mappedNamespaces:")
	assert.Contains(t, text, "- foo")
}
