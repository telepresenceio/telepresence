package setup

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// Summary is the one structured object the setup command reports: everything
// that was found, answered, proposed, and (with --apply) done.
type Summary struct {
	Facts          *ClusterFacts `json:"facts"`
	Answers        *Answers      `json:"answers"`
	Proposal       *Proposal     `json:"proposal"`
	Action         string        `json:"action"` // "would-install" / "would-upgrade" / "install" / "upgrade" / "none"
	PlannedObjects []string      `json:"plannedObjects,omitempty"`
	ApplyOutcome   string        `json:"applyOutcome,omitempty"`
	Verification   []Note        `json:"verification,omitempty"`
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
	if s.Proposal.Action == ActionInstall && len(s.PlannedObjects) > 0 {
		ioutil.Println(w, "This install will create:")
		for _, o := range s.PlannedObjects {
			ioutil.Printf(w, "  - %s\n", o)
		}
		ioutil.Println(w, "The release's resources are removed by 'telepresence helm uninstall'.")
		ioutil.Println(w, "If setup created the manager namespace, that namespace is left behind.")
	}
	PrintNotes(w, "Notes:", s.Proposal.Notes)
	ioutil.Printf(w, "Action: %s\n", s.Action)
	return nil
}

// Banner identifies the cluster a setup session is about to configure.
func Banner(facts *ClusterFacts) string {
	return fmt.Sprintf("Configuring cluster %q (server %s, manager namespace %s)",
		facts.Context, facts.Server, facts.ManagerNamespace)
}

// PrintNotes renders a section of note/warning lines; an empty list renders
// nothing.
func PrintNotes(w io.Writer, header string, notes []Note) {
	if len(notes) == 0 {
		return
	}
	ioutil.Println(w, header)
	for _, n := range notes {
		label := "note"
		if n.Level == NoteWarning {
			label = "warning"
		}
		ioutil.Printf(w, "  %s: %s\n", label, n.Text)
	}
}

func printFindings(w io.Writer, facts *ClusterFacts) {
	ioutil.Println(w, "Findings:")

	if facts.Context != "" || facts.Server != "" {
		area(w, "cluster", fmt.Sprintf("context %s, server %s", facts.Context, facts.Server), nil)
	}

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

	switch rs := &facts.Routing.Summary; rs.Verdict {
	case VerdictYes:
		area(w, "routing", "no conflicts", nil)
	case VerdictNo:
		area(w, "routing", fmt.Sprintf("%d conflicts", len(facts.Routing.Conflicts)), rs.Evidence)
	default:
		area(w, "routing", "unknown", rs.Evidence)
	}

	if facts.Release.Installed {
		area(w, "release", fmt.Sprintf("traffic-manager %s installed in namespace %s", facts.Release.Version, facts.Release.Namespace), nil)
	} else {
		area(w, "release", "not installed", nil)
	}

	if h := facts.Health; h != nil {
		healthArea(w, "traffic-manager deployment", &h.ManagerReady)
		healthArea(w, "agent-injector webhook", h.Webhook)
		healthArea(w, "webhook certificate", h.Certificate)
		healthArea(w, "agent-injector endpoints", h.InjectorEndpoints)
		healthArea(w, "quic endpoint", h.Quic)
		healthArea(w, "version skew", &h.VersionSkew)
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

func healthArea(w io.Writer, label string, f *Finding) {
	if f != nil {
		area(w, "health", fmt.Sprintf("%s %s", label, f.Verdict), f.Evidence)
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

// WriteValues writes the proposal's Helm values as YAML, preceded by the
// given provenance comment lines.
func WriteValues(w io.Writer, values map[string]any, header ...string) error {
	data, err := yaml.Marshal(values)
	if err != nil {
		return err
	}
	for _, h := range header {
		if _, err = io.WriteString(w, h+"\n"); err != nil {
			return err
		}
	}
	_, err = w.Write(data)
	return err
}

// WriteValuesFile writes the proposal's Helm values as YAML to a file.
func WriteValuesFile(path string, values map[string]any, header ...string) error {
	var buf bytes.Buffer
	if err := WriteValues(&buf, values, header...); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// ProvenanceHeader builds the comment lines that make a written values file
// explain itself: generating client, cluster, manager namespace, and date.
func ProvenanceHeader(facts *ClusterFacts, date time.Time) []string {
	return []string{
		"# Generated by telepresence setup " + version.Version,
		fmt.Sprintf("# Cluster: %s (%s)", facts.Context, facts.Server),
		"# Manager namespace: " + facts.ManagerNamespace,
		"# Date: " + date.Format(time.RFC3339),
	}
}
