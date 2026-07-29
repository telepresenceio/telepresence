// Package setup implements `telepresence setup`: a read-only survey of a
// cluster's viability for a traffic-manager installation, expressed as a
// ClusterFacts value, followed by an interview and a values proposal.
package setup

import (
	"context"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"helm.sh/helm/v3/pkg/release"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/routing"
)

// Verdict classifies a probe conclusion.
type Verdict string

const (
	VerdictYes      Verdict = "yes"      // confirmed by direct observation
	VerdictProbable Verdict = "probable" // inferred from strong signals
	VerdictNo       Verdict = "no"
	VerdictUnknown  Verdict = "unknown" // probe could not run (e.g. RBAC denied)
)

// Finding is a verdict together with the evidence that produced it.
type Finding struct {
	Verdict  Verdict  `json:"verdict"`
	Evidence []string `json:"evidence,omitempty"`
}

// ClusterFacts is the result of probing a cluster; it carries no client
// handles and is safe to marshal, diff, or hand to a pure decision function.
type ClusterFacts struct {
	Context          string          `json:"context,omitempty"`
	Server           string          `json:"server,omitempty"`
	ManagerNamespace string          `json:"managerNamespace"`
	NamespaceExists  bool            `json:"namespaceExists"`
	Privileges       PrivilegeFacts  `json:"privileges"`
	Quic             QuicFacts       `json:"quic"`
	NodeAgent        NodeAgentFacts  `json:"nodeAgent"`
	Webhook          WebhookFacts    `json:"webhook"`
	Namespaces       NamespaceFacts  `json:"namespaces"`
	Release          ReleaseFacts    `json:"release"`
	ClientAuth       ClientAuthFacts `json:"clientAuth"`       // the client's own credential kinds, as supplied by the caller
	Health           *HealthFacts    `json:"health,omitempty"` // read-only doctor checks; only when a release is installed
	ClientUpdate     UpdateFacts     `json:"clientUpdate"`
	Routing          RoutingFacts    `json:"routing"`
}

type PrivilegeFacts struct {
	ClusterWide                 Finding           `json:"clusterWide"`       // can create every object of a cluster-wide chart render
	Namespaced                  Finding           `json:"namespaced"`        // same for a namespace-scoped render (only evaluated when ClusterWide is not yes)
	Missing                     []string          `json:"missing,omitempty"` // itemized denials for the cluster-wide render, e.g. `create clusterroles.rbac.authorization.k8s.io`
	MissingNamespaced           []string          `json:"missingNamespaced,omitempty"`
	MissingAttributes           []DeniedAttribute `json:"missingAttributes,omitempty"`           // structured form of Missing
	MissingNamespacedAttributes []DeniedAttribute `json:"missingNamespacedAttributes,omitempty"` // structured form of MissingNamespaced
	// X509KubeSystem is whether the caller can create the kube-system
	// RoleBinding x509 client auth needs; consulted only when the decision
	// enables x509 auth.
	X509KubeSystem Finding `json:"x509KubeSystem"`
}

// ClientAuthFacts records which credential kinds the connecting client's own
// kubeconfig can produce.
type ClientAuthFacts struct {
	Bearer bool `json:"bearer"`
	X509   bool `json:"x509"`
}

type QuicFacts struct {
	Provider     string  `json:"provider,omitempty"` // gke|eks|aks|kind|k3s|unknown, from node providerID scheme
	LoadBalancer Finding `json:"loadBalancer"`       // a LoadBalancer Service would get an external endpoint
	NodePort     Finding `json:"nodePort"`           // NodePort discovery is viable (nodes listable, addresses present)
}

type NodeAgentFacts struct {
	Viable       Finding  `json:"viable"`
	LinuxNodes   int      `json:"linuxNodes"`
	TotalNodes   int      `json:"totalNodes"`
	Runtimes     []string `json:"runtimes,omitempty"` // distinct schemes from nodeInfo.containerRuntimeVersion, e.g. "containerd"
	Autopilot    bool     `json:"autopilot,omitempty"`
	CanaryDenial string   `json:"canaryDenial,omitempty"` // admission error when the dry-run canary was rejected
}

type WebhookFacts struct {
	CanCreate           Finding `json:"canCreate"`                     // create mutatingwebhookconfigurations
	ReachabilityConcern string  `json:"reachabilityConcern,omitempty"` // e.g. EKS with non-VPC CNI
}

type NamespaceFacts struct {
	Count      int    `json:"count"`
	ListDenied bool   `json:"listDenied,omitempty"`
	ListError  string `json:"listError,omitempty"` // non-RBAC failure to list namespaces
}

type ReleaseFacts struct {
	Installed bool           `json:"installed"`
	Version   string         `json:"version,omitempty"`
	Namespace string         `json:"namespace,omitempty"`
	Values    map[string]any `json:"values,omitempty"` // operator-supplied values (release.Config)
}

type UpdateFacts struct {
	Latest          string `json:"latest,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable,omitempty"`
	CheckError      string `json:"checkError,omitempty"`
}

// ProbePhases are the phases GatherFacts reports through Prober.Progress, in
// the order they occur.
var ProbePhases = []string{ //nolint:gochecknoglobals // immutable
	"Probing install privileges",
	"Probing QUIC viability",
	"Probing node-agent viability",
	"Probing webhook access",
	"Counting namespaces",
	"Looking for an existing installation",
	"Checking installation health",
	"Checking for a client update",
	"Checking for subnet conflicts",
}

// defaultUpdateCheckHost is the host queried for the client's own stable-release
// check when Prober.UpdateCheckHost is unset.
const defaultUpdateCheckHost = "app.getambassador.io"

// defaultHTTPClient is used for P7 when Prober.HTTPClient is unset; it is never
// mutated, so it is safe to share across Probers.
var defaultHTTPClient = &http.Client{Timeout: 5 * time.Second} //nolint:gochecknoglobals // immutable default

// DefaultCandidateValues returns the maximal feature set so the P1 RBAC sweep
// covers everything the tool might install.
func DefaultCandidateValues() map[string]any {
	return map[string]any{
		"agentInjector": map[string]any{"enabled": true},
		"nodeAgent":     map[string]any{"enabled": true},
		"quicTunnel":    map[string]any{"enabled": true},
	}
}

// Prober gathers ClusterFacts for one cluster/manager-namespace pair.
type Prober struct {
	KubeClient       kubernetes.Interface
	ManagerNamespace string
	Context          string          // kubeconfig context name, recorded in the facts
	Server           string          // API server URL, recorded in the facts
	CandidateValues  map[string]any  // values for the P1 chart render; nil means DefaultCandidateValues()
	UpdateCheckHost  string          // default "app.getambassador.io"
	HTTPClient       *http.Client    // default a client with a short timeout
	Progress         func(string)    // called with a phase description as each probe starts; nil is silent
	ClientAuth       ClientAuthFacts // the client's own credential kinds, recorded verbatim into the facts

	// ReleaseLookup finds an existing traffic-manager Helm release; nil disables
	// P6 and ReleaseFacts stays zero.
	ReleaseLookup func(ctx context.Context, namespace string) (*release.Release, error)

	// RouteSource reads the workstation's routing table; nil means
	// routing.GetRoutingTable.
	RouteSource func(ctx context.Context) ([]*routing.Route, error)
}

func (p *Prober) candidateValues() map[string]any {
	if p.CandidateValues != nil {
		return p.CandidateValues
	}
	return DefaultCandidateValues()
}

func (p *Prober) httpClient() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return defaultHTTPClient
}

// GatherFacts runs every probe and returns the resulting ClusterFacts. RBAC
// denials and admission rejections are recorded as facts, not returned as
// errors; only a failure that makes every probe meaningless (context
// cancellation, a transport failure while resolving the manager namespace)
// is returned as an error.
func (p *Prober) GatherFacts(ctx context.Context) (*ClusterFacts, error) {
	ctx = k8sapi.WithK8sInterface(ctx, p.KubeClient)

	nsExists, err := p.namespaceExists(ctx)
	if err != nil {
		return nil, err
	}

	nodes, nodesErr := p.listNodes(ctx)
	var provider string
	if nodesErr == nil {
		provider = classifyProvider(nodes)
	}

	facts := &ClusterFacts{
		Context:          p.Context,
		Server:           p.Server,
		ManagerNamespace: p.ManagerNamespace,
		NamespaceExists:  nsExists,
		ClientAuth:       p.ClientAuth,
	}
	p.progress("Probing install privileges")
	facts.Privileges = p.probeRBAC(ctx, nsExists)
	p.progress("Probing QUIC viability")
	facts.Quic = p.probeQuic(ctx, nodes, nodesErr, provider)
	p.progress("Probing node-agent viability")
	facts.NodeAgent = p.probeNodeAgent(ctx, nodes, nodesErr, provider, nsExists)
	p.progress("Probing webhook access")
	facts.Webhook = p.probeWebhook(ctx, provider)
	p.progress("Counting namespaces")
	facts.Namespaces = p.probeNamespaceScale(ctx)
	p.progress("Looking for an existing installation")
	facts.Release = p.probeRelease(ctx)
	p.progress("Checking installation health")
	if facts.Release.Installed {
		facts.Health = p.probeHealth(ctx, &facts.Release, p.ClientAuth)
	}
	p.progress("Checking for a client update")
	facts.ClientUpdate = p.probeUpdate(ctx)
	p.progress("Checking for subnet conflicts")
	facts.Routing = p.probeRouting(ctx, nodes)

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return facts, nil
}

func (p *Prober) progress(phase string) {
	if p.Progress != nil {
		p.Progress(phase)
	}
}

// namespaceExists reports whether the manager namespace exists. An RBAC
// denial leaves the answer as "false" (a fact, not an error); any other
// failure is treated as an infrastructure failure.
func (p *Prober) namespaceExists(ctx context.Context) (bool, error) {
	_, err := p.KubeClient.CoreV1().Namespaces().Get(ctx, p.ManagerNamespace, metav1.GetOptions{})
	switch {
	case err == nil:
		return true, nil
	case apierrors.IsNotFound(err):
		return false, nil
	case apierrors.IsForbidden(err):
		clog.Debugf(ctx, "cannot determine whether namespace %q exists: %v", p.ManagerNamespace, err)
		return false, nil
	default:
		return false, err
	}
}

// listNodes returns the cluster's nodes, or a non-nil error (typically an RBAC
// denial on a namespace-scoped install) that P2/P3 record as VerdictUnknown.
func (p *Prober) listNodes(ctx context.Context) ([]corev1.Node, error) {
	list, err := p.KubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}
