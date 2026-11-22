package rbacgen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"

	"helm.sh/helm/v3/pkg/release"
	auth "k8s.io/api/authorization/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/telepresenceio/telepresence/v2/pkg/authz"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

// CheckResult is the outcome of one client check.
type CheckResult string

const (
	// ResultAllowed means the operation is authorized.
	ResultAllowed CheckResult = "allowed"
	// ResultMissing means no alternative authorizes the operation.
	ResultMissing CheckResult = "missing"
	// ResultUnknown means the pods that the pods/portforward alternative is
	// reviewed on couldn't be listed.
	ResultUnknown CheckResult = "could not determine"
)

// Client operations, as they read in the output.
const (
	OpConnect   = "connect"
	OpTransport = "port-forward to the traffic-manager"
	OpRead      = "read namespace"
	OpIntercept = "intercept"
	OpIngest    = "ingest"
	OpLogs      = "logs"
	OpLogsYAML  = "logs/yaml"
)

// ClientCheck is the result of one review. Required checks decide whether
// the identity can use Telepresence. Intercept and the logs checks are
// reported, but an identity that may only ingest, or not gather logs, still
// passes.
type ClientCheck struct {
	Operation string      `json:"operation"`
	Namespace string      `json:"namespace"`
	Workload  string      `json:"workload,omitempty"`
	Required  bool        `json:"required"`
	Result    CheckResult `json:"result"`
	// Grant is the grant that authorized the operation.
	Grant   authz.Grant       `json:"grant,omitempty"`
	Missing []DeniedAttribute `json:"missing,omitempty"`
	Reason  string            `json:"reason,omitempty"`
}

// ClientMode holds the checks under one required grant.
type ClientMode struct {
	RequiredGrant authz.Grant   `json:"requiredGrant"`
	Checks        []ClientCheck `json:"checks"`
}

// ClientVerdict is the result of VerifyClient.
type ClientVerdict struct {
	ManagerNamespace string `json:"managerNamespace"`
	Identity         string `json:"identity,omitempty"`
	// Source says where the installed configuration came from.
	Source string   `json:"source"`
	Notes  []string `json:"notes,omitempty"`
	// Transport is nil when the traffic-manager publishes an external
	// endpoint, so that clients never port-forward to it.
	Transport   *ClientCheck  `json:"transport,omitempty"`
	Modes       []ClientMode  `json:"modes"`
	Diagnostics []ClientCheck `json:"diagnostics"`
}

// Satisfied reports whether every required check of the mode was allowed.
// A required check that couldn't be determined doesn't count.
func (m ClientMode) Satisfied() bool {
	for _, c := range m.Checks {
		if c.Required && c.Result != ResultAllowed {
			return false
		}
	}
	return true
}

// SatisfiedGrants returns the required grants whose checks all pass.
func (v *ClientVerdict) SatisfiedGrants() []authz.Grant {
	var gs []authz.Grant
	for _, m := range v.Modes {
		if m.Satisfied() {
			gs = append(gs, m.RequiredGrant)
		}
	}
	return gs
}

// OK reports whether the identity can use the traffic-manager. A required check
// that is missing or couldn't be determined fails it. When the installed
// configuration is unknown, the modes are alternatives, so it is enough that one
// of them passes.
func (v *ClientVerdict) OK() bool {
	if v.Transport != nil && v.Transport.Required && v.Transport.Result != ResultAllowed {
		return false
	}
	return len(v.SatisfiedGrants()) > 0
}

// VerifyClientRequest describes one run of `telepresence rbac verify client`.
type VerifyClientRequest struct {
	ManagerNamespace string
	Namespaces       []string
	Workloads        []string
	// Values are the values the traffic-manager is installed with. When nil,
	// they are read with ReleaseLookup, and when that fails too, the checks
	// run under every required grant.
	Values        *helm.Values
	ValuesSource  string
	ReleaseLookup func(ctx context.Context, namespace string) (*release.Release, error)
	// Client reviews access for the checked identity.
	Client kubernetes.Interface
	// WorkloadPods returns the pods of a workload, which the pods/portforward
	// alternative of a workload's reviews is grounded on. Nil, or an error,
	// means they can't be listed.
	WorkloadPods func(ctx context.Context, namespace, workload string) ([]string, error)
}

// ErrWorkloadNotFound is returned by WorkloadPodNames when no Deployment,
// StatefulSet or ReplicaSet has the given name.
var ErrWorkloadNotFound = errors.New("workload not found")

// errPodsUnknown is returned through Review.PodNames when the pods of a
// workload can't be listed and namespace-wide pods/portforward is denied.
var errPodsUnknown = errors.New("unable to list the pods of the workload")

// VerifyClient runs the traffic-manager's authorization reviews for the
// checked identity.
func VerifyClient(ctx context.Context, rq *VerifyClientRequest) (*ClientVerdict, error) {
	v := &ClientVerdict{ManagerNamespace: rq.ManagerNamespace}
	grants, external, err := installedConfig(ctx, rq, v)
	if err != nil {
		return nil, err
	}
	if id, err := ReadIdentity(ctx, rq.Client); err == nil && id != nil {
		v.Identity = id.Username
	}
	if rq.WorkloadPods != nil {
		for _, wl := range rq.Workloads {
			for _, ns := range rq.Namespaces {
				if _, err := rq.WorkloadPods(ctx, ns, wl); errors.Is(err, ErrWorkloadNotFound) {
					return nil, err
				}
			}
		}
	}
	if len(rq.Workloads) == 0 {
		v.Notes = append(v.Notes,
			"Only the unnamed reviews ran. Grants scoped to single workloads with resourceNames can't be seen without --workload.")
	}

	ck := newRecordingChecker(authz.SelfChecker(rq.Client))
	managerPod := ReleaseName + "-0"

	if external == nil || !*external {
		// With an unknown configuration, a missing port-forward is only reported,
		// since an external endpoint makes it unnecessary.
		c := ClientCheck{Operation: OpTransport, Namespace: rq.ManagerNamespace, Required: external != nil}
		ck.begin()
		allowed, err := authz.PortForwardAllowed(ctx, ck.check, rq.ManagerNamespace, []string{managerPod})
		if err != nil {
			return nil, err
		}
		c.finish(allowed, "", ck.end())
		if external == nil {
			c.Reason = "needed unless the traffic-manager publishes an external endpoint, which isn't known here"
		}
		v.Transport = &c
	}

	for _, g := range grants {
		mode := ClientMode{RequiredGrant: g}
		connect := authz.ConnectReview(rq.ManagerNamespace)
		connect.PodNames = func(context.Context) ([]string, error) { return []string{managerPod}, nil }
		c, err := runCheck(ctx, ck, g, connect, ClientCheck{Operation: OpConnect, Namespace: rq.ManagerNamespace, Required: true})
		if err != nil {
			return nil, err
		}
		mode.Checks = append(mode.Checks, c)

		for _, ns := range rq.Namespaces {
			var checks []struct {
				review *authz.Review
				check  ClientCheck
			}
			add := func(r *authz.Review, c ClientCheck) {
				checks = append(checks, struct {
					review *authz.Review
					check  ClientCheck
				}{r, c})
			}
			if len(rq.Workloads) == 0 {
				add(authz.NamespaceReadReview(ns), ClientCheck{Operation: OpRead, Namespace: ns, Required: true})
				add(authz.AttachmentReview(ns, "", "create"), ClientCheck{Operation: OpIntercept, Namespace: ns})
			}
			for _, wl := range rq.Workloads {
				add(groundOnPods(rq, ck, authz.EnsureAgentReview(ns, wl), ns, wl), ClientCheck{Operation: OpIngest, Namespace: ns, Workload: wl, Required: true})
				add(groundOnPods(rq, ck, authz.AttachmentReview(ns, wl, "create"), ns, wl), ClientCheck{Operation: OpIntercept, Namespace: ns, Workload: wl})
			}
			for _, rc := range checks {
				c, err := runCheck(ctx, ck, g, rc.review, rc.check)
				if err != nil {
					return nil, err
				}
				mode.Checks = append(mode.Checks, c)
			}
		}
		v.Modes = append(v.Modes, mode)
	}

	for _, ns := range rq.Namespaces {
		for _, d := range []struct{ op, sub string }{{OpLogs, ""}, {OpLogsYAML, "yaml"}} {
			c := ClientCheck{Operation: d.op, Namespace: ns}
			ck.begin()
			allowed, err := ck.check(ctx, authz.LogsAttributes(ns, d.sub))
			if err != nil {
				return nil, err
			}
			c.finish(allowed, "", ck.end())
			v.Diagnostics = append(v.Diagnostics, c)
		}
	}
	return v, nil
}

// installedConfig returns the required grants to check and whether the
// traffic-manager publishes an external endpoint, which is nil when that
// is unknown.
func installedConfig(ctx context.Context, rq *VerifyClientRequest, v *ClientVerdict) ([]authz.Grant, *bool, error) {
	values := rq.Values
	switch {
	case values != nil:
		v.Source = rq.ValuesSource
		if v.Source == "" {
			v.Source = "the given values"
		}
	case rq.ReleaseLookup == nil:
		v.Source = "unknown"
	default:
		rel, err := rq.ReleaseLookup(ctx, rq.ManagerNamespace)
		switch {
		case err != nil && (k8serrors.IsForbidden(err) || k8serrors.IsUnauthorized(err)):
			v.Source = "unknown"
			v.Notes = append(v.Notes, fmt.Sprintf(
				"Reading the traffic-manager release in namespace %s was refused, so the checks ran under every required grant. "+
					"Pass the values the traffic-manager is installed with as --input to check only the installed one.", rq.ManagerNamespace))
		case err != nil:
			return nil, nil, fmt.Errorf("unable to read the traffic-manager release in namespace %s: %w", rq.ManagerNamespace, err)
		case rel == nil:
			v.Source = "unknown"
			v.Notes = append(v.Notes, fmt.Sprintf(
				"There's no traffic-manager release in namespace %s, so the checks ran under every required grant.", rq.ManagerNamespace))
		default:
			values = &helm.Values{}
			if rel.Config != nil {
				if values, err = helm.ValuesFromMap(rel.Config); err != nil {
					return nil, nil, fmt.Errorf("unable to read the values of the traffic-manager release in namespace %s: %w", rq.ManagerNamespace, err)
				}
			}
			v.Source = "the values of the traffic-manager release in namespace " + rq.ManagerNamespace
		}
	}
	if values == nil {
		return []authz.Grant{authz.GrantPortForward, authz.GrantTelepresence, authz.GrantAny}, nil, nil
	}
	var g authz.Grant
	var text string
	if rg := values.Security.Authorization.RequiredGrant; rg != nil {
		text = *rg
	}
	if err := g.UnmarshalText([]byte(text)); err != nil {
		return nil, nil, err
	}
	external := false
	if e := values.ExternalEndpoint.Enabled; e != nil {
		external = *e
	}
	return []authz.Grant{g}, &external, nil
}

// groundOnPods grounds the pods/portforward alternative of r on the pods of
// the workload. When they can't be listed, it falls back to the
// namespace-wide review, and to errPodsUnknown when that is denied.
func groundOnPods(rq *VerifyClientRequest, ck *recordingChecker, r *authz.Review, namespace, workload string) *authz.Review {
	r.PodNames = func(ctx context.Context) ([]string, error) {
		if rq.WorkloadPods != nil {
			if pods, err := rq.WorkloadPods(ctx, namespace, workload); err == nil {
				return pods, nil
			}
		}
		allowed, err := ck.check(ctx, authz.PortForwardAttributes(namespace, ""))
		if err != nil {
			return nil, err
		}
		if allowed {
			return nil, nil
		}
		return nil, errPodsUnknown
	}
	return r
}

func runCheck(ctx context.Context, ck *recordingChecker, g authz.Grant, r *authz.Review, c ClientCheck) (ClientCheck, error) {
	ck.begin()
	verdict, err := authz.Evaluate(ctx, g, r, ck.check)
	denied := ck.end()
	switch {
	case errors.Is(err, errPodsUnknown):
		c.Result = ResultUnknown
		c.Reason = "the pods of the workload couldn't be listed, so pods/portforward on them couldn't be reviewed"
		return c, nil
	case err != nil:
		return c, err
	}
	c.finish(verdict.Allowed, verdict.Grant, denied)
	return c, nil
}

func (c *ClientCheck) finish(allowed bool, g authz.Grant, denied []*auth.ResourceAttributes) {
	if allowed {
		c.Result = ResultAllowed
		c.Grant = g
		return
	}
	c.Result = ResultMissing
	for _, ra := range denied {
		c.Missing = append(c.Missing, ToDeniedAttribute(ra))
	}
}

// recordingChecker memoizes reviews and records the attributes denied
// between begin and end. Reviews of single pods are not recorded, since the
// namespace-wide review names the same grant.
type recordingChecker struct {
	next authz.Checker

	mu     sync.Mutex
	cache  map[string]bool
	denied []*auth.ResourceAttributes
}

func newRecordingChecker(next authz.Checker) *recordingChecker {
	return &recordingChecker{next: next, cache: make(map[string]bool)}
}

func (r *recordingChecker) check(ctx context.Context, ra *auth.ResourceAttributes) (bool, error) {
	key := AttributeKey(ra)
	r.mu.Lock()
	allowed, ok := r.cache[key]
	r.mu.Unlock()
	if !ok {
		var err error
		if allowed, err = r.next(ctx, ra); err != nil {
			return false, err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[key] = allowed
	if !allowed && !(ra.Subresource == "portforward" && ra.Name != "") &&
		!slices.ContainsFunc(r.denied, func(d *auth.ResourceAttributes) bool { return AttributeKey(d) == key }) {
		r.denied = append(r.denied, ra)
	}
	return allowed, nil
}

func (r *recordingChecker) begin() {
	r.mu.Lock()
	r.denied = nil
	r.mu.Unlock()
}

func (r *recordingChecker) end() []*auth.ResourceAttributes {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.denied
	r.denied = nil
	return d
}

// WorkloadPodNames returns a VerifyClientRequest.WorkloadPods that finds the
// Deployment, StatefulSet or ReplicaSet called workload with client and
// lists its pods.
func WorkloadPodNames(client kubernetes.Interface) func(ctx context.Context, namespace, workload string) ([]string, error) {
	return func(ctx context.Context, namespace, workload string) ([]string, error) {
		apps := client.AppsV1()
		finders := []func() (*meta.LabelSelector, error){
			func() (*meta.LabelSelector, error) {
				d, err := apps.Deployments(namespace).Get(ctx, workload, meta.GetOptions{})
				if err != nil {
					return nil, err
				}
				return d.Spec.Selector, nil
			},
			func() (*meta.LabelSelector, error) {
				s, err := apps.StatefulSets(namespace).Get(ctx, workload, meta.GetOptions{})
				if err != nil {
					return nil, err
				}
				return s.Spec.Selector, nil
			},
			func() (*meta.LabelSelector, error) {
				rs, err := apps.ReplicaSets(namespace).Get(ctx, workload, meta.GetOptions{})
				if err != nil {
					return nil, err
				}
				return rs.Spec.Selector, nil
			},
		}
		var sel *meta.LabelSelector
		var errs []error
		for _, find := range finders {
			var err error
			if sel, err = find(); err == nil {
				break
			}
			errs = append(errs, err)
		}
		if sel == nil {
			if !slices.ContainsFunc(errs, func(err error) bool { return !k8serrors.IsNotFound(err) }) {
				return nil, fmt.Errorf("%w: no Deployment, StatefulSet or ReplicaSet %s in namespace %s", ErrWorkloadNotFound, workload, namespace)
			}
			return nil, fmt.Errorf("unable to find workload %s in namespace %s: %w", workload, namespace, errors.Join(errs...))
		}
		selector, err := meta.LabelSelectorAsSelector(sel)
		if err != nil {
			return nil, err
		}
		pods, err := client.CoreV1().Pods(namespace).List(ctx, meta.ListOptions{LabelSelector: selector.String()})
		if err != nil {
			return nil, err
		}
		names := make([]string, len(pods.Items))
		for i := range pods.Items {
			names[i] = pods.Items[i].Name
		}
		return names, nil
	}
}

// WriteText writes the verdict for a human reader.
func (v *ClientVerdict) WriteText(w io.Writer) {
	who := v.Identity
	if who == "" {
		who = "the checked identity"
	}
	ioutil.Printf(w, "Checked %s against the traffic-manager in namespace %s, configured by %s.\n", who, v.ManagerNamespace, v.Source)
	for _, n := range v.Notes {
		ioutil.Println(w, n)
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if v.Transport != nil {
		ioutil.Println(tw, "\nTransport:")
		writeCheck(tw, v.Transport)
	}
	for _, m := range v.Modes {
		ioutil.Printf(tw, "\nRequired grant %q:\n", m.RequiredGrant)
		for i := range m.Checks {
			writeCheck(tw, &m.Checks[i])
		}
	}
	if len(v.Diagnostics) > 0 {
		ioutil.Println(tw, "\nDiagnostics:")
		for i := range v.Diagnostics {
			writeCheck(tw, &v.Diagnostics[i])
		}
	}
	_ = tw.Flush()
	if v.OK() {
		if len(v.Modes) > 1 {
			names := make([]string, 0, len(v.Modes))
			for _, g := range v.SatisfiedGrants() {
				names = append(names, fmt.Sprintf("%q", g))
			}
			ioutil.Printf(w, "\nThe installed required grant isn't known. Nothing required is missing under %s.\n", strings.Join(names, " or "))
		} else {
			ioutil.Println(w, "\nNothing required is missing.")
		}
	} else {
		ioutil.Println(w, "\nSome required permissions are missing. telepresence rbac generate client prints the roles that grant them.")
	}
}

func writeCheck(w io.Writer, c *ClientCheck) {
	what := c.Operation
	if c.Workload != "" {
		what += " " + c.Workload
	}
	if !c.Required {
		what += " (optional)"
	}
	result := string(c.Result)
	if c.Grant != "" {
		result += " by the " + string(c.Grant) + " grant"
	}
	ioutil.Printf(w, "  %s\tin %s\t%s\n", what, c.Namespace, result)
	for i, d := range c.Missing {
		switch {
		case i > 0:
			ioutil.Printf(w, "    \t\tor %s\n", d)
		case len(c.Missing) > 1:
			ioutil.Printf(w, "    \t\tneeds one of %s\n", d)
		default:
			ioutil.Printf(w, "    \t\tneeds %s\n", d)
		}
	}
	if c.Reason != "" {
		ioutil.Printf(w, "    \t\t%s\n", c.Reason)
	}
}
