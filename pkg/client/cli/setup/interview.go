package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/slice"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// Tri is a three-state override for a probe verdict.
type Tri string

const (
	TriAuto Tri = "auto"
	TriOn   Tri = "on"
	TriOff  Tri = "off"
)

// ParseTri validates a Tri value ("auto", "on", or "off").
func ParseTri(s string) (Tri, error) {
	switch t := Tri(s); t {
	case TriAuto, TriOn, TriOff:
		return t, nil
	default:
		return "", errcat.User.Newf("invalid value %q: must be auto, on, or off", s)
	}
}

// ManagedScope is the traffic-manager's namespace limiting strategy.
type ManagedScope string

const (
	ManagedScopeAll        ManagedScope = "all"
	ManagedScopeNamespaces ManagedScope = "namespaces"
	ManagedScopeSelector   ManagedScope = "selector"
)

// ParseManagedScope validates a ManagedScope value ("all", "namespaces", or
// "selector").
func ParseManagedScope(s string) (ManagedScope, error) {
	switch c := ManagedScope(s); c {
	case ManagedScopeAll, ManagedScopeNamespaces, ManagedScopeSelector:
		return c, nil
	default:
		return "", errcat.User.Newf("invalid managed scope %q: must be all, namespaces, or selector", s)
	}
}

// Answers holds the interview's conclusions, whether they came from an
// --input pin, a prompt, or a default.
type Answers struct {
	Attach              bool               `json:"attach"`
	Replace             bool               `json:"replace,omitempty"`
	UpgradeManager      bool               `json:"upgradeManager,omitempty"`
	ManagedScope        ManagedScope       `json:"managedScope"`
	ManagedNamespaces   []string           `json:"managedNamespaces,omitempty"` // for managed scope namespaces
	SelectorLabels      map[string]string  `json:"selectorLabels,omitempty"`    // for managed scope selector
	MappedNamespaces    []string           `json:"mappedNamespaces,omitempty"`  // becomes the clients' mapped-namespaces default
	Quic                Tri                `json:"quic"`
	NodeAgent           Tri                `json:"nodeAgent"`
	AllowConflicts      bool               `json:"allowConflicts,omitempty"` // accept routing conflicts cluster-wide
	EnforceAuth         bool               `json:"enforceAuth"`
	RequiredGrant       string             `json:"requiredGrant,omitempty"` // any|telepresence|portforward
	ExternalEndpoint    bool               `json:"externalEndpoint,omitempty"`
	ExternalTLSSecret   string             `json:"externalTlsSecret,omitempty"`
	ExternalCertManager *CertManagerAnswer `json:"externalCertManager,omitempty"`
	LegacyAccess        bool               `json:"legacyAccess"`
}

// CertManagerAnswer names the cert-manager issuer and DNS names chosen for
// the external endpoint's certificate.
type CertManagerAnswer struct {
	IssuerName string   `json:"issuerName"`
	IssuerKind string   `json:"issuerKind"`
	DNSNames   []string `json:"dnsNames,omitempty"`
}

// Preset records which Answers fields were already decided by an --input
// pin; a preset answer is never asked.
type Preset struct {
	Attach            bool
	Replace           bool
	ManagedScope      bool
	ManagedNamespaces bool
	MappedNamespaces  bool
	AllowConflicts    bool
	EnforceAuth       bool
	RequiredGrant     bool
	ExternalEndpoint  bool
	LegacyAccess      bool
}

// RequiredGrant values name which grant the traffic-manager requires for
// authorization.
const (
	RequiredGrantAny          = "any"
	RequiredGrantTelepresence = "telepresence"
	RequiredGrantPortForward  = "portforward"
)

// ParseRequiredGrant validates a required-grant value ("any", "telepresence",
// or "portforward").
func ParseRequiredGrant(s string) (string, error) {
	switch s {
	case RequiredGrantAny, RequiredGrantTelepresence, RequiredGrantPortForward:
		return s, nil
	default:
		return "", errcat.User.Newf("invalid required grant %q: must be any, telepresence, or portforward", s)
	}
}

// promptAttempts bounds how many invalid answers a single question tolerates
// before the interview gives up.
const promptAttempts = 3

// scopeRecommendLimit is the namespace count at which the scope question
// starts recommending a limited scope.
const scopeRecommendLimit = 30

// Interviewer asks the gated questions that the probed facts make relevant.
// Prompts read one line each from In; an empty line takes the default. When
// NonInteractive is set, nothing is asked and every question takes its
// default.
type Interviewer struct {
	Facts          *ClusterFacts
	In             io.Reader
	Out            io.Writer
	NonInteractive bool
	Answers        Answers
	Preset         Preset

	br        *bufio.Reader
	effective *helm.Values
}

// Interview fills in the answers that were not pinned by an --input file and
// returns the completed set.
func (iv *Interviewer) Interview(ctx context.Context) (*Answers, error) {
	defaults, err := helm.DefaultValues()
	if err != nil {
		return nil, err
	}
	iv.effective = helm.MergeValues(iv.Facts.Release.Values, defaults)

	a := iv.Answers
	if a.Quic == "" {
		a.Quic = TriAuto
	}
	if a.NodeAgent == "" {
		a.NodeAgent = TriAuto
	}

	if !iv.Preset.Attach {
		v, err := iv.askYesNo("Will clients attach to workloads (intercept/replace/ingest/wiretap)? ", true)
		if err != nil {
			return nil, err
		}
		a.Attach = v
	}

	naMaybe := iv.Facts.NodeAgent.Viable.Verdict.Likely()
	if a.Attach && naMaybe && a.NodeAgent != TriOff && !iv.Preset.Replace {
		v, err := iv.askYesNo("Will you use the replace command? ", false)
		if err != nil {
			return nil, err
		}
		a.Replace = v
	}

	if iv.Facts.ClientUpdate.UpdateAvailable {
		ioutil.Printf(iv.Out, "A newer telepresence client %s is available (this client is %s).\n",
			iv.Facts.ClientUpdate.Latest, version.Version)
	}

	if iv.Facts.Release.Installed {
		switch iv.Facts.releaseAge(version.Structured) {
		case releaseNewer:
			ioutil.Printf(iv.Out, "The installed traffic-manager %s is newer than this client (%s); consider upgrading the client instead.\n",
				iv.Facts.Release.Version, version.Version)
		case releaseOlder:
			v, err := iv.askYesNo(fmt.Sprintf("traffic-manager %s is installed; client is %s. Upgrade the traffic-manager? ",
				iv.Facts.Release.Version, version.Version), true)
			if err != nil {
				return nil, err
			}
			a.UpgradeManager = v
		case releaseUnparseable:
			v, err := iv.askYesNo(fmt.Sprintf("traffic-manager version %q could not be parsed (assuming it is older); client is %s. Upgrade the traffic-manager? ",
				iv.Facts.Release.Version, version.Version), true)
			if err != nil {
				return nil, err
			}
			a.UpgradeManager = v
		case releaseSame, releaseAbsent:
		}
	}

	if !iv.Preset.ManagedScope {
		scope, err := iv.askManagedScope()
		if err != nil {
			return nil, err
		}
		a.ManagedScope = scope
	}
	if a.ManagedScope == "" {
		a.ManagedScope = ManagedScopeAll
	}
	if err := iv.completeManagedScope(&a); err != nil {
		return nil, err
	}

	if err := iv.askSecurity(&a); err != nil {
		return nil, err
	}

	if conflicts := iv.Facts.Routing.ConflictingSubnets(); len(conflicts) > 0 && !iv.Preset.AllowConflicts {
		v, err := iv.askYesNo(fmt.Sprintf(
			"Local routes overlap the cluster's subnets (%s). Allow the conflicts cluster-wide (traffic to those ranges goes to the cluster for every client)? ",
			strings.Join(conflicts, ", ")), false)
		if err != nil {
			return nil, err
		}
		a.AllowConflicts = v
	}

	return &a, ctx.Err()
}

// completeManagedScope fills in the namespace list or selector that the
// chosen managed scope requires but the answers did not already supply.
func (iv *Interviewer) completeManagedScope(a *Answers) error {
	switch a.ManagedScope {
	case ManagedScopeNamespaces:
		if len(a.ManagedNamespaces) == 0 {
			if iv.NonInteractive {
				a.ManagedNamespaces = []string{iv.Facts.ManagerNamespace}
			} else {
				nss, err := iv.askList("Namespaces to manage (comma-separated): ")
				if err != nil {
					return err
				}
				a.ManagedNamespaces = nss
			}
		}
		a.ManagedNamespaces = slice.AppendUnique(a.ManagedNamespaces, iv.Facts.ManagerNamespace)
	case ManagedScopeSelector:
		if len(a.SelectorLabels) == 0 {
			if iv.NonInteractive {
				return errcat.User.New("--managed-scope=selector requires an interactive session to enter the label selector")
			}
			labels, err := iv.askLabels("Namespace label selector (key=value, comma-separated): ")
			if err != nil {
				return err
			}
			a.SelectorLabels = labels
		}
	case ManagedScopeAll:
	}
	return nil
}

// askSecurity asks the authentication, external endpoint, required grant,
// and legacy client access questions that the presets leave open.
func (iv *Interviewer) askSecurity(a *Answers) error {
	if !iv.Preset.EnforceAuth {
		def := iv.enforceAuthDefault()
		if !iv.NonInteractive {
			iv.explainEnforceAuth()
		}
		v, err := iv.askYesNo("Enforce caller authentication? ", def)
		if err != nil {
			return err
		}
		a.EnforceAuth = v
	}

	if a.EnforceAuth && !iv.Preset.ExternalEndpoint {
		if err := iv.askExternalEndpoint(a); err != nil {
			return err
		}
	}

	if a.EnforceAuth && !iv.Preset.RequiredGrant {
		grant, err := iv.askRequiredGrant(a)
		if err != nil {
			return err
		}
		a.RequiredGrant = grant
	}

	if !iv.Preset.LegacyAccess {
		def := iv.legacyAccessDefault()
		v, err := iv.askYesNo("Do clients older than 2.32 need to connect to this traffic-manager? ", def)
		if err != nil {
			return err
		}
		a.LegacyAccess = v
	}
	return nil
}

// legacyAccessDefault is the "Do clients older than 2.32 need to connect?"
// default: false on a fresh install (the chart default only applies once a
// release is installed), else the installed release's own setting when
// present, else the chart's own default.
func (iv *Interviewer) legacyAccessDefault() bool {
	if !iv.Facts.Release.Installed {
		return false
	}
	return deref(iv.effective.ClientRbac.LegacyAccess)
}

// enforceAuthDefault is the "Enforce caller authentication?" default: on a
// fresh install, whether this caller's kubeconfig has a bearer token or a
// client certificate; otherwise the installed release's own setting, or the
// chart's default when the release does not set it.
func (iv *Interviewer) enforceAuthDefault() bool {
	if !iv.Facts.Release.Installed {
		auth := iv.Facts.ClientAuth
		return auth.Bearer || auth.X509
	}
	return iv.effective.AuthEnforced()
}

// explainEnforceAuth prints why the enforce-auth question matters and how
// this caller's own kubeconfig fares under it, just before the prompt.
func (iv *Interviewer) explainEnforceAuth() {
	ioutil.Println(iv.Out, "Enforcing means the traffic-manager refuses telepresence clients older than")
	ioutil.Println(iv.Out, "2.31. It is also required for Direct Connect, where clients reach the")
	ioutil.Println(iv.Out, "traffic-manager at a published address instead of through the Kubernetes API.")
	ioutil.Println(iv.Out, iv.callerAuthLine())

	if iv.Facts.Release.Installed && iv.Facts.Release.Values.AuthEnforced() {
		ioutil.Println(iv.Out, "The installed traffic-manager already enforces authentication.")
	}
}

// callerAuthLine reports whether this caller's own kubeconfig would pass the
// enforce-auth check, and why not when it would not.
func (iv *Interviewer) callerAuthLine() string {
	auth := iv.Facts.ClientAuth
	switch {
	case auth.Bearer:
		return "Your own client will keep working: your kubeconfig has a bearer token."
	case auth.X509 && iv.Facts.Privileges.X509KubeSystem.Verdict != VerdictNo:
		return "Your own client will keep working: your kubeconfig has a client certificate."
	case auth.X509:
		return "Your own client will keep working once an admin applies this: verifying your client certificate needs a " +
			"RoleBinding in kube-system that you are not allowed to create."
	default:
		return "Your own client would be locked out: your kubeconfig has neither a token nor a client certificate."
	}
}

// askExternalEndpoint asks whether to enable Direct Connect, then, on yes,
// which certificate it should use. It is a no-op when neither a TLS Secret
// nor cert-manager is available and the installed release does not already
// enable the endpoint.
func (iv *Interviewer) askExternalEndpoint(a *Answers) error {
	cmAvailable := iv.Facts.External.CertManager.Verdict.Likely()
	secrets := iv.Facts.External.TLSSecrets

	def := deref(iv.effective.ExternalEndpoint.Enabled)
	if !cmAvailable && len(secrets) == 0 && !def {
		return nil
	}

	v, err := iv.askYesNo(
		"Enable Direct Connect, so clients reach the traffic-manager at a published address instead of through the Kubernetes API? ",
		def)
	if err != nil {
		return err
	}
	a.ExternalEndpoint = v
	if !v || externalTLSSecretName(iv.Facts.Release.Values) != "" {
		return nil
	}

	secretName, useCertManager, err := iv.askExternalCertificate(secrets, cmAvailable)
	if err != nil {
		return err
	}
	if useCertManager {
		cma, err := iv.askCertManagerAnswer()
		if err != nil {
			return err
		}
		a.ExternalCertManager = cma
	} else {
		a.ExternalTLSSecret = secretName
	}
	return nil
}

// externalCertCandidate is one entry in the external endpoint's certificate
// choice: an existing TLS Secret, or a new certificate issued by cert-manager.
type externalCertCandidate struct {
	label       string
	secretName  string
	certManager bool
}

// externalCertCandidates lists the TLS Secrets, in order, followed by the
// cert-manager option when it is available.
func externalCertCandidates(secrets []TLSSecretFacts, certManagerAvailable bool) []externalCertCandidate {
	cands := make([]externalCertCandidate, 0, len(secrets)+1)
	for i := range secrets {
		cands = append(cands, externalCertCandidate{label: secrets[i].candidateLine(), secretName: secrets[i].Name})
	}
	if certManagerAvailable {
		cands = append(cands, externalCertCandidate{label: "a new one issued by cert-manager", certManager: true})
	}
	return cands
}

// externalCertDefault picks the default TLS Secret candidate (1-based; 0
// means no default): the chart's own cert-manager target when present, else
// the only Secret when there is exactly one; never an expired or expiring one.
func externalCertDefault(secrets []TLSSecretFacts) int {
	if len(secrets) == 0 || secrets[0].Expired || secrets[0].ExpiringSoon {
		return 0
	}
	if secrets[0].Name == certManagerSecretName || len(secrets) == 1 {
		return 1
	}
	return 0
}

// candidateLine renders one TLS Secret candidate line: the quoted Secret name
// followed by detail.
func (s *TLSSecretFacts) candidateLine() string {
	return fmt.Sprintf("Secret %q", s.Name) + s.detail()
}

// askExternalCertificate picks the certificate the external endpoint uses. A
// single candidate is taken without asking only when it is cert-manager or a
// Secret that is neither expired nor expiring soon; otherwise the choice is
// presented with the computed default, and a non-interactive run with no
// default errors instead of guessing among the candidates. An empty
// candidate list errors rather than being indexed.
func (iv *Interviewer) askExternalCertificate(secrets []TLSSecretFacts, certManagerAvailable bool) (secretName string, useCertManager bool, err error) {
	cands := externalCertCandidates(secrets, certManagerAvailable)
	if len(cands) == 0 {
		return "", false, errcat.User.New(
			"the external endpoint has no TLS Secret or cert-manager available for its certificate")
	}
	soleSecretExpiring := !cands[0].certManager && len(secrets) == 1 && (secrets[0].Expired || secrets[0].ExpiringSoon)
	if len(cands) == 1 && !soleSecretExpiring {
		return cands[0].secretName, cands[0].certManager, nil
	}
	def := externalCertDefault(secrets)

	if iv.NonInteractive {
		if def == 0 {
			if len(cands) == 1 {
				return "", false, errcat.User.New(
					"the only TLS Secret found for the external endpoint is expired or expiring soon; " +
						"externalEndpoint.tls.secretName must be pinned in the input to use it anyway")
			}
			return "", false, errcat.User.New(
				"several TLS Secrets were found for the external endpoint; externalEndpoint.tls.secretName must be pinned in the input")
		}
		c := cands[def-1]
		return c.secretName, c.certManager, nil
	}

	ioutil.Println(iv.Out, "The endpoint needs a TLS certificate that clients can trust. Which one should it use?")
	for i, c := range cands {
		ioutil.Printf(iv.Out, "  %d) %s\n", i+1, c.label)
	}
	prompt := fmt.Sprintf("Choose 1-%d: ", len(cands))
	if def != 0 {
		prompt = fmt.Sprintf("Choose 1-%d [%d]: ", len(cands), def)
	}
	choice, err := iv.askChoice(prompt, len(cands), def)
	if err != nil {
		return "", false, err
	}
	c := cands[choice-1]
	return c.secretName, c.certManager, nil
}

// askCertManagerAnswer asks for the cert-manager issuer and DNS names the
// external endpoint's certificate needs; it requires an interactive session.
func (iv *Interviewer) askCertManagerAnswer() (*CertManagerAnswer, error) {
	if iv.NonInteractive {
		return nil, errcat.User.New(
			"an external endpoint certificate issued by cert-manager requires an interactive session to enter the issuer and DNS names")
	}
	name, err := iv.askRequired("cert-manager issuer name: ")
	if err != nil {
		return nil, err
	}
	kind, err := iv.askIssuerKind("cert-manager issuer kind (Issuer or ClusterIssuer) [ClusterIssuer]: ")
	if err != nil {
		return nil, err
	}
	dnsNames, err := iv.askList("DNS names clients will use to reach the endpoint (comma-separated): ")
	if err != nil {
		return nil, err
	}
	return &CertManagerAnswer{IssuerName: name, IssuerKind: kind, DNSNames: dnsNames}, nil
}

// requiredGrantIndex maps a requiredGrant value to its 1-based choice index,
// or 0 when the value is not one of the three known grants.
func requiredGrantIndex(s string) int {
	switch s {
	case RequiredGrantAny:
		return 1
	case RequiredGrantTelepresence:
		return 2
	case RequiredGrantPortForward:
		return 3
	default:
		return 0
	}
}

// requiredGrantChoice returns the requiredGrant value at the given 1-based
// choice index.
func requiredGrantChoice(idx int) string {
	return [...]string{RequiredGrantAny, RequiredGrantTelepresence, RequiredGrantPortForward}[idx-1]
}

// askRequiredGrant presents the required-grant choice, defaulting to the
// installed release's own setting when it names one of the three grants,
// else to "telepresence" when the external endpoint was chosen on a fresh
// install, else the chart's own default.
func (iv *Interviewer) askRequiredGrant(a *Answers) (string, error) {
	def := 1
	if grant := deref(iv.effective.Security.Authorization.RequiredGrant); grant != "" {
		if idx := requiredGrantIndex(grant); idx != 0 {
			def = idx
		}
	}
	if !iv.Facts.Release.Installed && a.ExternalEndpoint {
		def = 2
	}

	if iv.NonInteractive {
		return requiredGrantChoice(def), nil
	}

	ioutil.Println(iv.Out, "Which grant should the traffic-manager require for authorization?")
	ioutil.Println(iv.Out, "  1) any (either grant satisfies the check)")
	ioutil.Println(iv.Out,
		"  2) telepresence (Telepresence's own policy grants; clients lose direct traffic-agent port-forwards unless QUIC is enabled)")
	ioutil.Println(iv.Out, "  3) portforward (the pods/portforward permission)")
	choice, err := iv.askChoice(fmt.Sprintf("Choose 1-3 [%d]: ", def), 3, def)
	if err != nil {
		return "", err
	}
	return requiredGrantChoice(choice), nil
}

// askManagedScope presents the numbered managed-scope choice, defaulting to a
// namespace list when the probes concluded that a cluster-wide install is
// impossible.
func (iv *Interviewer) askManagedScope() (ManagedScope, error) {
	clusterWideDenied := iv.Facts.Privileges.ClusterWide.Verdict == VerdictNo
	if iv.NonInteractive {
		if clusterWideDenied {
			return ManagedScopeNamespaces, nil
		}
		return ManagedScopeAll, nil
	}

	switch {
	case iv.Facts.Namespaces.ListDenied:
		ioutil.Println(iv.Out, "Namespace listing was denied.")
	case iv.Facts.Namespaces.ListError != "":
		ioutil.Printf(iv.Out, "The namespace count is unknown: %s\n", iv.Facts.Namespaces.ListError)
	default:
		ioutil.Printf(iv.Out, "The cluster has %d namespaces.\n", iv.Facts.Namespaces.Count)
		if iv.Facts.Namespaces.Count >= scopeRecommendLimit {
			ioutil.Println(iv.Out, "Limiting the traffic-manager's scope is recommended for a cluster of this size.")
		}
	}
	def := 1
	if clusterWideDenied && iv.Facts.Privileges.Namespaced.Verdict == VerdictYes {
		ioutil.Println(iv.Out, "A cluster-wide install looks impossible with the current privileges; a namespace-limited install is recommended.")
		def = 2
	}
	ioutil.Println(iv.Out, "Which namespaces should the traffic-manager manage?")
	ioutil.Println(iv.Out, `  1) no limit (cluster-wide)`)
	ioutil.Println(iv.Out, `  2) managed namespace list (Helm value "namespaces")`)
	ioutil.Println(iv.Out, `  3) namespace label selector (Helm value "namespaceSelector")`)
	choice, err := iv.askChoice(fmt.Sprintf("Choose 1-3 [%d]: ", def), 3, def)
	if err != nil {
		return "", err
	}
	return [...]ManagedScope{ManagedScopeAll, ManagedScopeNamespaces, ManagedScopeSelector}[choice-1], nil
}

// askUntilValid prints prompt, reads one line, and applies parse to it,
// reprompting with retry up to promptAttempts times on a line parse rejects.
// It gives up with a "too many invalid answers" error past that limit.
func askUntilValid[T any](iv *Interviewer, prompt string, parse func(line string) (T, bool), retry string) (T, error) {
	var zero T
	for range promptAttempts {
		ioutil.WriteString(iv.Out, prompt)
		line, err := iv.readLine()
		if err != nil {
			return zero, err
		}
		if v, ok := parse(line); ok {
			return v, nil
		}
		ioutil.Println(iv.Out, retry)
	}
	return zero, errcat.User.New("too many invalid answers")
}

// askYesNo asks question, appending the def-appropriate "[Y/n] "/"[y/N] "
// suffix, and takes def on an empty line or when NonInteractive.
func (iv *Interviewer) askYesNo(question string, def bool) (bool, error) {
	if iv.NonInteractive {
		return def, nil
	}
	suffix := "[y/N] "
	if def {
		suffix = "[Y/n] "
	}
	return askUntilValid(iv, question+suffix, func(line string) (bool, bool) {
		switch strings.ToLower(line) {
		case "":
			return def, true
		case "y", "yes":
			return true, true
		case "n", "no":
			return false, true
		default:
			return false, false
		}
	}, "Please answer y or n.")
}

// askChoice asks for a number between 1 and limit. def is 1-based; 0 means
// there is no default, so an empty line is reprompted instead of accepted.
func (iv *Interviewer) askChoice(prompt string, limit, def int) (int, error) {
	if iv.NonInteractive {
		return def, nil
	}
	retry := fmt.Sprintf("Please answer a number between 1 and %d.", limit)
	return askUntilValid(iv, prompt, func(line string) (int, bool) {
		if line == "" && def != 0 {
			return def, true
		}
		if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= limit {
			return n, true
		}
		return 0, false
	}, retry)
}

func (iv *Interviewer) askList(prompt string) ([]string, error) {
	return askUntilValid(iv, prompt, func(line string) ([]string, bool) {
		nss := splitList(line)
		return nss, len(nss) > 0
	}, "Please enter at least one name.")
}

func (iv *Interviewer) askLabels(prompt string) (map[string]string, error) {
	return askUntilValid(iv, prompt, parseLabels, "Please enter key=value pairs, separated by commas.")
}

// askRequired asks for a non-empty line of text.
func (iv *Interviewer) askRequired(prompt string) (string, error) {
	return askUntilValid(iv, prompt, func(line string) (string, bool) {
		return line, line != ""
	}, "Please enter a value.")
}

// askIssuerKind asks for a cert-manager issuer kind, defaulting to
// ClusterIssuer on an empty line.
func (iv *Interviewer) askIssuerKind(prompt string) (string, error) {
	return askUntilValid(iv, prompt, func(line string) (string, bool) {
		switch line {
		case "":
			return "ClusterIssuer", true
		case "Issuer", "ClusterIssuer":
			return line, true
		default:
			return "", false
		}
	}, "Please answer Issuer or ClusterIssuer.")
}

// readLine reads one trimmed line; end of input is treated as an empty
// answer so a closed stdin degrades to defaults instead of an error.
func (iv *Interviewer) readLine() (string, error) {
	if iv.br == nil {
		iv.br = bufio.NewReader(iv.In)
	}
	line, err := iv.br.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseLabels(s string) (map[string]string, bool) {
	parts := splitList(s)
	if len(parts) == 0 {
		return nil, false
	}
	labels := make(map[string]string, len(parts))
	for _, p := range parts {
		k, v, ok := strings.Cut(p, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			return nil, false
		}
		labels[k] = v
	}
	return labels, true
}
