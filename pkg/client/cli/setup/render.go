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

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
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

	if s.Proposal.Values != nil {
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
func (f *ClusterFacts) Banner() string {
	return fmt.Sprintf("Configuring cluster %q (server %s, manager namespace %s)",
		f.Context, f.Server, f.ManagerNamespace)
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

	area(w, "authentication", fmt.Sprintf("this client has %s; kube-system x509 RoleBinding %s",
		credentialDescription(facts.ClientAuth), pf.X509KubeSystem.Verdict), pf.X509KubeSystem.Evidence)

	q := &facts.Quic
	area(w, "quic", fmt.Sprintf("provider %s, loadBalancer %s, nodePort %s", providerDisplay(orUnknown(q.Provider)), q.LoadBalancer.Verdict, q.NodePort.Verdict),
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
		area(w, "routing", "no conflicts", rs.Evidence)
	case VerdictNo:
		area(w, "routing", fmt.Sprintf("%d conflicts", len(facts.Routing.Conflicts)), rs.Evidence)
	default:
		area(w, "routing", "unknown", rs.Evidence)
	}

	if facts.Release.Installed {
		line := fmt.Sprintf("traffic-manager %s installed in namespace %s", facts.Release.Version, facts.Release.Namespace)
		if facts.Release.Workload == "Deployment" {
			line += ", running as a Deployment"
		}
		var evidence []string
		if facts.Release.ValuesError != "" {
			evidence = []string{"values: " + facts.Release.ValuesError}
		}
		area(w, "release", line, evidence)
	} else {
		area(w, "release", "not installed", nil)
	}

	externalArea(w, &facts.External)

	if h := facts.Health; h != nil {
		healthArea(w, "traffic-manager", &h.ManagerReady)
		healthArea(w, "agent-injector webhook", h.Webhook)
		healthArea(w, "webhook certificate", h.Certificate)
		healthArea(w, "agent-injector endpoints", h.InjectorEndpoints)
		healthArea(w, "quic endpoint", h.Quic)
		healthArea(w, "x509 client auth", h.X509ClientAuth)
		healthArea(w, "external endpoint", h.ExternalEndpoint)
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

// credentialDescription names the credential kinds this client's kubeconfig
// can produce, the way the authentication finding line reports them.
func credentialDescription(auth ClientAuthFacts) string {
	switch {
	case auth.Bearer && auth.X509:
		return "bearer token and client certificate"
	case auth.Bearer:
		return "bearer token"
	case auth.X509:
		return "client certificate"
	default:
		return "no usable credential"
	}
}

// externalArea prints the external endpoint's prerequisites: whether
// cert-manager is installed, and the TLS Secrets already in the manager
// namespace.
func externalArea(w io.Writer, ext *ExternalFacts) {
	secretsPart := fmt.Sprintf("%d TLS secrets", len(ext.TLSSecrets))
	switch {
	case ext.SecretsListDenied:
		secretsPart = "TLS secrets: listing denied"
	case ext.SecretsListError != "":
		secretsPart = "TLS secrets: unknown"
	}
	evidence := make([]string, 0, len(ext.TLSSecrets))
	for _, s := range ext.TLSSecrets {
		evidence = append(evidence, s.evidenceLine())
	}
	area(w, "external endpoint", fmt.Sprintf("cert-manager %s, %s", ext.CertManager.Verdict, secretsPart), evidence)
}

// evidenceLine renders one TLS Secret evidence line: its name followed by
// detail.
func (s *TLSSecretFacts) evidenceLine() string {
	return s.Name + s.detail()
}

// detail renders a TLS Secret's DNS names and expiry, each preceded by two
// spaces, or its ReadError when the leaf certificate could not be read.
func (s *TLSSecretFacts) detail() string {
	if s.ReadError != "" {
		return "  " + s.ReadError
	}
	var detail string
	if len(s.DNSNames) > 0 {
		detail += "  " + strings.Join(s.DNSNames, ",")
	}
	if t, err := time.Parse(time.RFC3339, s.NotAfter); err == nil {
		detail += "  expires " + t.Format("2006-01-02")
	}
	switch {
	case s.Expired:
		detail += " (expired)"
	case s.ExpiringSoon:
		detail += " (expires within 30 days)"
	}
	return detail
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
func WriteValues(w io.Writer, values *helm.Values, header ...string) error {
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
func WriteValuesFile(path string, values *helm.Values, header ...string) error {
	var buf bytes.Buffer
	if err := WriteValues(&buf, values, header...); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// ProvenanceHeader builds the comment lines that make a written values file
// explain itself: generating client, cluster, manager namespace, and date.
func (f *ClusterFacts) ProvenanceHeader(date time.Time) []string {
	return []string{
		"# Generated by telepresence setup " + version.Version,
		fmt.Sprintf("# Cluster: %s (%s)", f.Context, f.Server),
		"# Manager namespace: " + f.ManagerNamespace,
		"# Date: " + date.Format(time.RFC3339),
	}
}
