package setup

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestPrintReport_PlannedObjectsSection(t *testing.T) {
	s := renderSummary()
	s.PlannedObjects = []string{"ClusterRole traffic-manager-ambassador", "Deployment traffic-manager.ambassador"}

	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	require.NoError(t, PrintReport(cmd, s))
	text := out.String()
	assert.Contains(t, text, "This install will create:")
	assert.Contains(t, text, "  - Deployment traffic-manager.ambassador")
	assert.Contains(t, text, "removed by 'telepresence helm uninstall'")

	// Anything but an install renders no will-create section.
	s.Proposal.Action = ActionUpgrade
	out.Reset()
	require.NoError(t, PrintReport(cmd, s))
	assert.NotContains(t, out.String(), "This install will create:")
}

func TestPrintReport_ClusterLine(t *testing.T) {
	s := renderSummary()
	s.Facts.Context = "kind-dev"
	s.Facts.Server = "https://127.0.0.1:6443"

	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	require.NoError(t, PrintReport(cmd, s))
	assert.Contains(t, out.String(), "  cluster: context kind-dev, server https://127.0.0.1:6443")
}

func TestBanner(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Context = "kind-dev"
		f.Server = "https://127.0.0.1:6443"
	})
	assert.Equal(t, `Configuring cluster "kind-dev" (server https://127.0.0.1:6443, manager namespace ambassador)`, Banner(facts))
}

func TestNextStepsWanted(t *testing.T) {
	assert.True(t, NextStepsWanted(ActionInstall, true, false, true), "a successful apply wants next steps")
	assert.True(t, NextStepsWanted(ActionNone, false, true, true), "validation over a healthy install wants next steps")
	assert.False(t, NextStepsWanted(ActionInstall, false, false, true), "a plain would-install does not")
	assert.False(t, NextStepsWanted(ActionNone, false, false, true), "action none without an install does not")
	assert.False(t, NextStepsWanted(ActionNone, false, true, false), "an unhealthy install does not")
}

func TestHealthFactsClean(t *testing.T) {
	var h *HealthFacts
	assert.True(t, h.Clean(), "no health checks is clean")
	h = &HealthFacts{
		ManagerReady: Finding{Verdict: VerdictYes},
		VersionSkew:  Finding{Verdict: VerdictUnknown},
	}
	assert.True(t, h.Clean(), "unknown findings do not make an install unhealthy")
	h.Quic = &Finding{Verdict: VerdictNo}
	assert.False(t, h.Clean())
}

func TestPrintReport_HealthLines(t *testing.T) {
	s := renderSummary()
	quic := Finding{Verdict: VerdictYes, Evidence: []string{"QUIC endpoint allocated node port 31234"}}
	s.Facts.Health = &HealthFacts{
		ManagerReady: Finding{Verdict: VerdictNo, Evidence: []string{"0 of 1 replicas ready", "BackOff: image pull failure"}},
		Quic:         &quic,
		VersionSkew:  Finding{Verdict: VerdictYes, Evidence: []string{"client and traffic-manager are both 2.31.0"}},
	}
	out := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	require.NoError(t, PrintReport(cmd, s))
	text := out.String()
	assert.Contains(t, text, "  health: traffic-manager deployment no")
	assert.Contains(t, text, "    - BackOff: image pull failure")
	assert.Contains(t, text, "  health: quic endpoint yes")
	assert.Contains(t, text, "  health: version skew yes")
	assert.NotContains(t, text, "agent-injector webhook", "absent optional findings render no line")
}

func TestPrintNextSteps(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Workloads.Samples = []WorkloadSample{{Name: "echo-easy", Namespace: "default", Port: 8080}}
	})
	out := &bytes.Buffer{}
	PrintNextSteps(out, facts, "default")
	text := out.String()
	assert.Contains(t, text, "Next steps:")
	assert.Contains(t, text, "  telepresence connect -n default")
	assert.Contains(t, text, "  telepresence list")
	assert.Contains(t, text, "  telepresence intercept echo-easy --port 8080")

	t.Run("sample without port omits the port flag", func(t *testing.T) {
		facts.Workloads.Samples[0].Port = 0
		out := &bytes.Buffer{}
		PrintNextSteps(out, facts, "default")
		assert.Contains(t, out.String(), "  telepresence intercept echo-easy\n")
	})
	t.Run("no sample omits the intercept line", func(t *testing.T) {
		facts.Workloads.Samples = nil
		out := &bytes.Buffer{}
		PrintNextSteps(out, facts, "default")
		assert.NotContains(t, out.String(), "intercept")
	})
}

func TestWriteValues_ProvenanceHeader(t *testing.T) {
	facts := recFacts(func(f *ClusterFacts) {
		f.Context = "kind-dev"
		f.Server = "https://127.0.0.1:6443"
	})
	header := ProvenanceHeader(facts, time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC))
	values := map[string]any{"nodeAgent": map[string]any{"enabled": true}}

	buf := &bytes.Buffer{}
	require.NoError(t, WriteValues(buf, values, header...))
	text := buf.String()
	assert.Contains(t, text, "# Generated by telepresence setup ")
	assert.Contains(t, text, "# Cluster: kind-dev (https://127.0.0.1:6443)")
	assert.Contains(t, text, "# Manager namespace: ambassador")
	assert.Contains(t, text, "# Date: 2026-07-19T12:00:00Z")

	// The header must survive an --input round trip: YAML comments are ignored.
	path := filepath.Join(t.TempDir(), "values.yaml")
	require.NoError(t, WriteValuesFile(path, values, header...))
	got, err := LoadInputValues(path)
	require.NoError(t, err)
	assert.Equal(t, values, got)
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
