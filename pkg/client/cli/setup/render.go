package setup

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// Summary is the one structured object the setup command reports: everything
// that was found, answered, and proposed.
type Summary struct {
	Facts    *ClusterFacts `json:"facts"`
	Answers  *Answers      `json:"answers"`
	Proposal *Proposal     `json:"proposal"`
	Action   string        `json:"action"` // "would-install" / "would-upgrade" / "install" / "upgrade" / "none"
}

// ActionWord renders a proposal action in the state-manifest verb style:
// without --apply the action becomes "would-<action>".
func ActionWord(a Action, apply bool) string {
	if !apply && a != ActionNone {
		return "would-" + string(a)
	}
	return string(a)
}

// PrintReport writes the summary: one structured object when formatted output
// was requested, otherwise sectioned text.
func PrintReport(cmd *cobra.Command, s *Summary) error {
	if output.WantsFormatted(cmd) {
		output.Object(cmd.Context(), s, true)
		return nil
	}
	w := cmd.OutOrStdout()
	printFindings(w, s.Facts)

	if len(s.Proposal.Values) > 0 {
		data, err := yaml.Marshal(s.Proposal.Values)
		if err != nil {
			return err
		}
		ioutil.Println(w, "Proposed configuration:")
		writeIndented(w, string(data))
		if len(s.Proposal.ChangedKeys) > 0 {
			ioutil.Println(w, "Changed from current installation:")
			for _, k := range s.Proposal.ChangedKeys {
				ioutil.Printf(w, "  - %s\n", k)
			}
		}
	}
	if len(s.Proposal.Notes) > 0 {
		ioutil.Println(w, "Notes:")
		for _, n := range s.Proposal.Notes {
			label := "note"
			if n.Level == NoteWarning {
				label = "warning"
			}
			ioutil.Printf(w, "  %s: %s\n", label, n.Text)
		}
	}
	ioutil.Printf(w, "Action: %s\n", s.Action)
	return nil
}

func printFindings(w io.Writer, facts *ClusterFacts) {
	ioutil.Println(w, "Findings:")

	pf := &facts.Privileges
	area(w, "privileges", fmt.Sprintf("cluster-wide install %s, namespaced install %s", pf.ClusterWide.Verdict, pf.Namespaced.Verdict),
		concat(pf.ClusterWide.Evidence, prefixed("missing: ", pf.Missing)))

	q := &facts.Quic
	area(w, "quic", fmt.Sprintf("provider %s, loadBalancer %s, nodePort %s", orUnknown(q.Provider), q.LoadBalancer.Verdict, q.NodePort.Verdict),
		concat(q.LoadBalancer.Evidence, q.NodePort.Evidence))

	na := &facts.NodeAgent
	naLine := fmt.Sprintf("%s (%d of %d nodes linux", na.Viable.Verdict, na.LinuxNodes, na.TotalNodes)
	if len(na.Runtimes) > 0 {
		naLine += ", runtimes: " + strings.Join(na.Runtimes, ", ")
	}
	naLine += ")"
	naEvidence := na.Viable.Evidence
	if na.CanaryDenial != "" {
		naEvidence = concat(naEvidence, []string{"admission canary rejected: " + na.CanaryDenial})
	}
	area(w, "node-agent", naLine, naEvidence)

	wh := &facts.Webhook
	whEvidence := wh.CanCreate.Evidence
	if wh.ReachabilityConcern != "" {
		whEvidence = concat(whEvidence, []string{wh.ReachabilityConcern})
	}
	area(w, "webhook", fmt.Sprintf("create %s", wh.CanCreate.Verdict), whEvidence)

	switch {
	case facts.Namespaces.ListDenied:
		area(w, "namespaces", "listing denied", nil)
	case facts.Namespaces.ListError != "":
		area(w, "namespaces", "unknown", []string{facts.Namespaces.ListError})
	default:
		area(w, "namespaces", fmt.Sprintf("%d", facts.Namespaces.Count), nil)
	}

	if facts.Release.Installed {
		area(w, "release", fmt.Sprintf("traffic-manager %s installed in namespace %s", facts.Release.Version, facts.Release.Namespace), nil)
	} else {
		area(w, "release", "not installed", nil)
	}

	cu := &facts.ClientUpdate
	switch {
	case cu.UpdateAvailable:
		area(w, "client update", fmt.Sprintf("%s available (this client is %s)", cu.Latest, version.Version), nil)
	case cu.CheckError != "":
		area(w, "client update", "check failed", []string{cu.CheckError})
	default:
		area(w, "client update", "up to date", nil)
	}
}

func area(w io.Writer, name, conclusion string, evidence []string) {
	ioutil.Printf(w, "  %s: %s\n", name, conclusion)
	for _, e := range evidence {
		ioutil.Printf(w, "    - %s\n", e)
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func prefixed(prefix string, ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = prefix + s
	}
	return out
}

func concat(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	return append(append(make([]string, 0, len(a)+len(b)), a...), b...)
}

func writeIndented(w io.Writer, text string) {
	for line := range strings.Lines(text) {
		ioutil.WriteString(w, "  "+line)
	}
	if !strings.HasSuffix(text, "\n") {
		ioutil.WriteString(w, "\n")
	}
}

// WriteValues writes the proposal's Helm values as YAML.
func WriteValues(w io.Writer, values map[string]any) error {
	data, err := yaml.Marshal(values)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// WriteValuesFile writes the proposal's Helm values as YAML to a file.
func WriteValuesFile(path string, values map[string]any) error {
	var buf bytes.Buffer
	if err := WriteValues(&buf, values); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
