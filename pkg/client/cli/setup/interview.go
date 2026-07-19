package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// Tri is a three-state override for a probe verdict.
type Tri string

const (
	TriAuto Tri = "auto"
	TriOn   Tri = "on"
	TriOff  Tri = "off"
)

// ParseTri validates a --quic/--node-agent flag value.
func ParseTri(s string) (Tri, error) {
	switch t := Tri(s); t {
	case TriAuto, TriOn, TriOff:
		return t, nil
	default:
		return "", errcat.User.Newf("invalid value %q: must be auto, on, or off", s)
	}
}

// ScopeChoice is the namespace limiting strategy.
type ScopeChoice string

const (
	ScopeAll        ScopeChoice = "all"
	ScopeNamespaces ScopeChoice = "namespaces"
	ScopeSelector   ScopeChoice = "selector"
	ScopeMapped     ScopeChoice = "mapped"
)

// ParseScope validates a --scope flag value.
func ParseScope(s string) (ScopeChoice, error) {
	switch c := ScopeChoice(s); c {
	case ScopeAll, ScopeNamespaces, ScopeSelector, ScopeMapped:
		return c, nil
	default:
		return "", errcat.User.Newf("invalid scope %q: must be all, namespaces, selector, or mapped", s)
	}
}

// Answers holds the interview's conclusions, whether they came from flags,
// prompts, or defaults.
type Answers struct {
	Attach             bool                `json:"attach"`
	Replace            bool                `json:"replace,omitempty"`
	UpgradeManager     bool                `json:"upgradeManager,omitempty"`
	Scope              ScopeChoice         `json:"scope"`
	ManagedNamespaces  []string            `json:"managedNamespaces,omitempty"` // for scope namespaces|mapped
	SelectorLabels     map[string]string   `json:"selectorLabels,omitempty"`    // for scope selector
	Quic               Tri                 `json:"quic"`
	NodeAgent          Tri                 `json:"nodeAgent"`
	AllowConflicts     bool                `json:"allowConflicts,omitempty"` // accept routing conflicts cluster-wide
	ClientRbac         bool                `json:"clientRbac,omitempty"`     // grant non-admin users the RBAC to use telepresence
	ClientRbacSubjects []ClientRbacSubject `json:"clientRbacSubjects,omitempty"`
}

// Preset records which Answers fields were set by command-line flags or input
// pins; a preset answer is never asked.
type Preset struct {
	Attach            bool
	Replace           bool
	UpgradeManager    bool
	Scope             bool
	ManagedNamespaces bool
	AllowConflicts    bool
	ClientRbac        bool
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

	br *bufio.Reader
}

// Interview fills in the answers that were not preset by flags and returns
// the completed set.
func (iv *Interviewer) Interview(ctx context.Context) (*Answers, error) {
	a := iv.Answers
	if a.Quic == "" {
		a.Quic = TriAuto
	}
	if a.NodeAgent == "" {
		a.NodeAgent = TriAuto
	}

	if !iv.Preset.Attach {
		v, err := iv.askYesNo("Will clients attach to workloads (intercept/replace/ingest/wiretap)? [Y/n] ", true)
		if err != nil {
			return nil, err
		}
		a.Attach = v
	}

	naMaybe := iv.Facts.NodeAgent.Viable.Verdict == VerdictYes || iv.Facts.NodeAgent.Viable.Verdict == VerdictProbable
	if a.Attach && naMaybe && a.NodeAgent != TriOff && !iv.Preset.Replace {
		v, err := iv.askYesNo("Will you use the replace command? [y/N] ", false)
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
		switch compareRelease(iv.Facts, version.Structured) {
		case releaseNewer:
			ioutil.Printf(iv.Out, "The installed traffic-manager %s is newer than this client (%s); consider upgrading the client instead.\n",
				iv.Facts.Release.Version, version.Version)
		case releaseOlder:
			if !iv.Preset.UpgradeManager {
				v, err := iv.askYesNo(fmt.Sprintf("traffic-manager %s is installed; client is %s. Upgrade the traffic-manager? [Y/n] ",
					iv.Facts.Release.Version, version.Version), true)
				if err != nil {
					return nil, err
				}
				a.UpgradeManager = v
			}
		case releaseUnparseable:
			if !iv.Preset.UpgradeManager {
				v, err := iv.askYesNo(fmt.Sprintf("traffic-manager version %q could not be parsed (assuming it is older); client is %s. Upgrade the traffic-manager? [Y/n] ",
					iv.Facts.Release.Version, version.Version), true)
				if err != nil {
					return nil, err
				}
				a.UpgradeManager = v
			}
		case releaseSame, releaseAbsent:
		}
	}

	if !iv.Preset.Scope {
		scope, err := iv.askScope()
		if err != nil {
			return nil, err
		}
		a.Scope = scope
	}
	if a.Scope == "" {
		a.Scope = ScopeAll
	}
	if err := iv.completeScope(&a); err != nil {
		return nil, err
	}

	if conflicts := iv.Facts.Routing.ConflictingSubnets(); len(conflicts) > 0 && !iv.Preset.AllowConflicts {
		v, err := iv.askYesNo(fmt.Sprintf(
			"Local routes overlap the cluster's subnets (%s). Allow the conflicts cluster-wide (traffic to those ranges goes to the cluster for every client)? [y/N] ",
			strings.Join(conflicts, ", ")), false)
		if err != nil {
			return nil, err
		}
		a.AllowConflicts = v
	}

	return &a, ctx.Err()
}

// completeScope fills in the namespace list or selector that the chosen scope
// requires but the flags did not supply.
func (iv *Interviewer) completeScope(a *Answers) error {
	switch a.Scope {
	case ScopeNamespaces:
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
		a.ManagedNamespaces = ensureContains(a.ManagedNamespaces, iv.Facts.ManagerNamespace)
	case ScopeMapped:
		if len(a.ManagedNamespaces) == 0 {
			if iv.NonInteractive {
				return errcat.User.New("--scope=mapped requires --managed-namespaces in non-interactive mode")
			}
			nss, err := iv.askList("Namespaces delivered to clients as their mapped-namespaces default (comma-separated): ")
			if err != nil {
				return err
			}
			a.ManagedNamespaces = nss
		}
	case ScopeSelector:
		if len(a.SelectorLabels) == 0 {
			if iv.NonInteractive {
				return errcat.User.New("--scope=selector requires an interactive session to enter the label selector")
			}
			labels, err := iv.askLabels("Namespace label selector (key=value, comma-separated): ")
			if err != nil {
				return err
			}
			a.SelectorLabels = labels
		}
	case ScopeAll:
	}
	return nil
}

// askScope presents the numbered scope choice, defaulting to a namespace
// list when the probes concluded that a cluster-wide install is impossible.
func (iv *Interviewer) askScope() (ScopeChoice, error) {
	clusterWideDenied := iv.Facts.Privileges.ClusterWide.Verdict == VerdictNo
	if iv.NonInteractive {
		if clusterWideDenied {
			return ScopeNamespaces, nil
		}
		return ScopeAll, nil
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
	ioutil.Println(iv.Out, "How should the traffic-manager's scope be limited?")
	ioutil.Println(iv.Out, `  1) no limit (cluster-wide)`)
	ioutil.Println(iv.Out, `  2) managed namespace list (Helm value "namespaces")`)
	ioutil.Println(iv.Out, `  3) namespace label selector (Helm value "namespaceSelector")`)
	ioutil.Println(iv.Out, `  4) mapped-namespaces default delivered to clients (Helm value "client.cluster.mappedNamespaces")`)
	choice, err := iv.askChoice(fmt.Sprintf("Choose 1-4 [%d]: ", def), 4, def)
	if err != nil {
		return "", err
	}
	return [...]ScopeChoice{ScopeAll, ScopeNamespaces, ScopeSelector, ScopeMapped}[choice-1], nil
}

func (iv *Interviewer) askYesNo(prompt string, def bool) (bool, error) {
	if iv.NonInteractive {
		return def, nil
	}
	for range promptAttempts {
		ioutil.WriteString(iv.Out, prompt)
		line, err := iv.readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(line) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		ioutil.Println(iv.Out, "Please answer y or n.")
	}
	return false, errcat.User.New("too many invalid answers")
}

func (iv *Interviewer) askChoice(prompt string, limit, def int) (int, error) {
	if iv.NonInteractive {
		return def, nil
	}
	for range promptAttempts {
		ioutil.WriteString(iv.Out, prompt)
		line, err := iv.readLine()
		if err != nil {
			return 0, err
		}
		if line == "" {
			return def, nil
		}
		if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= limit {
			return n, nil
		}
		ioutil.Printf(iv.Out, "Please answer a number between 1 and %d.\n", limit)
	}
	return 0, errcat.User.New("too many invalid answers")
}

func (iv *Interviewer) askList(prompt string) ([]string, error) {
	for range promptAttempts {
		ioutil.WriteString(iv.Out, prompt)
		line, err := iv.readLine()
		if err != nil {
			return nil, err
		}
		if nss := splitList(line); len(nss) > 0 {
			return nss, nil
		}
		ioutil.Println(iv.Out, "Please enter at least one name.")
	}
	return nil, errcat.User.New("too many invalid answers")
}

func (iv *Interviewer) askLabels(prompt string) (map[string]string, error) {
	for range promptAttempts {
		ioutil.WriteString(iv.Out, prompt)
		line, err := iv.readLine()
		if err != nil {
			return nil, err
		}
		if labels, ok := parseLabels(line); ok {
			return labels, nil
		}
		ioutil.Println(iv.Out, "Please enter key=value pairs, separated by commas.")
	}
	return nil, errcat.User.New("too many invalid answers")
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

func ensureContains(nss []string, ns string) []string {
	for _, n := range nss {
		if n == ns {
			return nss
		}
	}
	return append(nss, ns)
}
