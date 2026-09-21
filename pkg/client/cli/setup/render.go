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
	_, privilegesLine, privilegesEvidence := privilegesSummary(pf)
	area(w, "privileges", privilegesLine, privilegesEvidence)

	area(w, "authentication", fmt.Sprintf("this client has %s; kube-system x509 RoleBinding %s",
		credentialDescription(facts.ClientAuth), pf.X509KubeSystem.Verdict), pf.X509KubeSystem.Evidence)

	_, quicLine, quicEvidence := quicSummary(&facts.Quic)
	area(w, "quic", quicLine, quicEvidence)

	_, naLine, naEvidence := nodeAgentSummary(&facts.NodeAgent)
	area(w, "node-agent", naLine, naEvidence)

	_, whLine, whEvidence := webhookSummary(&facts.Webhook)
	area(w, "webhook", whLine, whEvidence)

	_, nsLine, nsEvidence := namespacesSummary(&facts.Namespaces)
	area(w, "namespaces", nsLine, nsEvidence)

	_, routingLine, routingEvidence := routingSummary(&facts.Routing)
	area(w, "routing", routingLine, routingEvidence)

	_, releaseLine, releaseEvidence := releaseSummary(&facts.Release)
	area(w, "release", releaseLine, releaseEvidence)

	externalArea(w, &facts.External)

	if h := facts.Health; h != nil {
		for _, lf := range healthLabeledFindings(h) {
			healthArea(w, lf.label, lf.finding)
		}
	}

	_, cuLine, cuEvidence := clientUpdateSummary(&facts.ClientUpdate)
	area(w, "client update", cuLine, cuEvidence)
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
	_, line, evidence := externalSummary(ext)
	area(w, "external endpoint", line, evidence)
}

// externalSummary reports cert-manager's availability and the TLS Secrets
// already in the manager namespace; its verdict is cert-manager's, the
// prerequisite that decides whether the external endpoint can get a
// certificate without one being supplied.
func externalSummary(ext *ExternalFacts) (Verdict, string, []string) {
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
	return ext.CertManager.Verdict, fmt.Sprintf("cert-manager %s, %s", ext.CertManager.Verdict, secretsPart), evidence
}

// privilegesSummary reports whether either a cluster-wide or a namespaced
// install is viable; its verdict is yes if either is, no if neither is, and
// otherwise the stronger of the two intermediate verdicts.
func privilegesSummary(pf *PrivilegeFacts) (Verdict, string, []string) {
	summary := fmt.Sprintf("cluster-wide install %s, namespaced install %s", pf.ClusterWide.Verdict, pf.Namespaced.Verdict)
	evidence := concat(pf.ClusterWide.Evidence, prefixed("missing: ", pf.Missing))
	return combineVerdicts(pf.ClusterWide.Verdict, pf.Namespaced.Verdict), summary, evidence
}

// quicSummary reports whether either QUIC discovery path is viable; its
// verdict follows the same either-path rule as privilegesSummary.
func quicSummary(q *QuicFacts) (Verdict, string, []string) {
	summary := fmt.Sprintf("provider %s, loadBalancer %s, nodePort %s",
		providerDisplay(orUnknown(q.Provider)), q.LoadBalancer.Verdict, q.NodePort.Verdict)
	evidence := concat(q.LoadBalancer.Evidence, q.NodePort.Evidence)
	return combineVerdicts(q.LoadBalancer.Verdict, q.NodePort.Verdict), summary, evidence
}

// combineVerdicts reports the strongest of two independent viability
// verdicts: yes if either is yes, otherwise the better of the two.
func combineVerdicts(a, b Verdict) Verdict {
	switch {
	case a == VerdictYes || b == VerdictYes:
		return VerdictYes
	case a == VerdictProbable || b == VerdictProbable:
		return VerdictProbable
	case a == VerdictUnknown || b == VerdictUnknown:
		return VerdictUnknown
	default:
		return VerdictNo
	}
}

// nodeAgentSummary reports the node-agent viability finding verbatim; it is
// the phase's own Finding.
func nodeAgentSummary(na *NodeAgentFacts) (Verdict, string, []string) {
	line := fmt.Sprintf("%s (%d of %d nodes linux", na.Viable.Verdict, na.LinuxNodes, na.TotalNodes)
	if len(na.Runtimes) > 0 {
		line += ", runtimes: " + strings.Join(na.Runtimes, ", ")
	}
	line += ")"
	evidence := na.Viable.Evidence
	if na.CanaryDenial != "" {
		evidence = concat(evidence, []string{"admission canary rejected: " + na.CanaryDenial})
	}
	return na.Viable.Verdict, line, evidence
}

// webhookSummary reports the webhook-creation finding verbatim; it is the
// phase's own Finding.
func webhookSummary(wh *WebhookFacts) (Verdict, string, []string) {
	evidence := wh.CanCreate.Evidence
	if wh.ReachabilityConcern != "" {
		evidence = concat(evidence, []string{wh.ReachabilityConcern})
	}
	return wh.CanCreate.Verdict, fmt.Sprintf("create %s", wh.CanCreate.Verdict), evidence
}

// namespacesSummary reports the namespace count as a neutral fact; it is
// unknown when the count itself could not be established.
func namespacesSummary(nf *NamespaceFacts) (Verdict, string, []string) {
	switch {
	case nf.ListDenied:
		return VerdictUnknown, "listing denied", nil
	case nf.ListError != "":
		return VerdictUnknown, "unknown", []string{nf.ListError}
	default:
		return VerdictYes, fmt.Sprintf("%d", nf.Count), nil
	}
}

// routingSummary reports the subnet-conflict finding verbatim; it is the
// phase's own Finding.
func routingSummary(rf *RoutingFacts) (Verdict, string, []string) {
	rs := &rf.Summary
	switch rs.Verdict {
	case VerdictYes:
		return VerdictYes, "no conflicts", rs.Evidence
	case VerdictNo:
		return VerdictNo, fmt.Sprintf("%d conflicts", len(rf.Conflicts)), rs.Evidence
	default:
		return rs.Verdict, "unknown", rs.Evidence
	}
}

// releaseSummary reports whether a traffic-manager release was found, a
// neutral fact regardless of which way it comes out.
func releaseSummary(rf *ReleaseFacts) (Verdict, string, []string) { //nolint:unparam // always yes: a neutral fact, kept for a uniform summary-function signature
	if !rf.Installed {
		return VerdictYes, "not installed", nil
	}
	line := fmt.Sprintf("traffic-manager %s installed in namespace %s", rf.Version, rf.Namespace)
	if rf.Workload == "Deployment" {
		line += ", running as a Deployment"
	}
	var evidence []string
	if rf.ValuesError != "" {
		evidence = []string{"values: " + rf.ValuesError}
	}
	return VerdictYes, line, evidence
}

// clientUpdateSummary reports whether this client is current; yes when it
// is, probable when a newer release is available, unknown when the check
// itself failed.
func clientUpdateSummary(cu *UpdateFacts) (Verdict, string, []string) {
	switch {
	case cu.UpdateAvailable:
		return VerdictProbable, fmt.Sprintf("%s available (this client is %s)", cu.Latest, version.Version), nil
	case cu.CheckError != "":
		return VerdictUnknown, "check failed", []string{cu.CheckError}
	default:
		return VerdictYes, "up to date", nil
	}
}

// healthLabeledFindings lists the health findings in the order printFindings
// prints them, paired with the label their area line uses.
func healthLabeledFindings(h *HealthFacts) []struct {
	label   string
	finding *Finding
} {
	return []struct {
		label   string
		finding *Finding
	}{
		{"traffic-manager", &h.ManagerReady},
		{"agent-injector webhook", h.Webhook},
		{"webhook certificate", h.Certificate},
		{"agent-injector endpoints", h.InjectorEndpoints},
		{"quic endpoint", h.Quic},
		{"x509 client auth", h.X509ClientAuth},
		{"external endpoint", h.ExternalEndpoint},
		{"version skew", &h.VersionSkew},
	}
}

// healthSeverity orders verdicts from best (yes) to worst (no), so the
// worst health finding can be picked out as the phase's overall verdict.
func healthSeverity(v Verdict) int {
	switch v {
	case VerdictNo:
		return 3
	case VerdictUnknown:
		return 2
	case VerdictProbable:
		return 1
	default:
		return 0
	}
}

// healthSummary reports the worst of the installation's health findings, or
// a neutral "not installed" when there is no release to check.
func healthSummary(rf *ReleaseFacts, h *HealthFacts) (Verdict, string, []string) {
	if !rf.Installed || h == nil {
		return VerdictYes, "not installed", nil
	}
	worst := VerdictYes
	var worstLabel string
	var worstEvidence []string
	for _, lf := range healthLabeledFindings(h) {
		if lf.finding == nil {
			continue
		}
		if healthSeverity(lf.finding.Verdict) > healthSeverity(worst) {
			worst = lf.finding.Verdict
			worstLabel = lf.label
			worstEvidence = lf.finding.Evidence
		}
	}
	if worstLabel == "" {
		return VerdictYes, "ready", nil
	}
	return worst, fmt.Sprintf("%s %s", worstLabel, worst), worstEvidence
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
