package rbacgen

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/release"
	rbac "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// ClientFormat selects the output of GenerateClient.
type ClientFormat string

const (
	// FormatRBAC emits RBAC manifests.
	FormatRBAC ClientFormat = "rbac"
	// FormatValues emits a clientRbac values block.
	FormatValues ClientFormat = "values"
)

// ParseClientFormat parses the value of an --emit flag.
func ParseClientFormat(s string) (ClientFormat, error) {
	switch f := ClientFormat(s); f {
	case FormatRBAC, FormatValues:
		return f, nil
	default:
		return "", fmt.Errorf("invalid format %q, must be %q or %q", s, FormatRBAC, FormatValues)
	}
}

const nameLabel = "kubernetes.io/metadata.name"

// ClientRequest describes one run of `telepresence rbac generate client`.
type ClientRequest struct {
	ManagerNamespace string
	Subjects         []Subject
	// Namespaces limits the output to these namespaces. Empty means every
	// namespace the traffic-manager manages.
	Namespaces []string
	// Values are the values the traffic-manager is installed with. When nil,
	// they are read from the release with ReleaseLookup.
	Values        *helm.Values
	ValuesSource  string
	ReleaseLookup func(ctx context.Context, namespace string) (*release.Release, error)
	// Client reads the labels of the requested namespaces when the
	// traffic-manager selects its namespaces dynamically. It may be nil.
	Client kubernetes.Interface
	Format ClientFormat
}

// ClientOutput is the result of GenerateClient.
type ClientOutput struct {
	Header []string
	Body   []byte
}

// WriteTo writes the header as YAML comments followed by the body.
func (o *ClientOutput) WriteTo(w io.Writer) (int64, error) {
	return writeCommented(w, o.Header, o.Body)
}

// ClientName is the name of the generated namespace roles and bindings.
// The connect and logs roles get it with a -connect and -logs suffix.
func ClientName(managerNamespace string) string {
	return "telepresence-client-" + managerNamespace
}

// GenerateClient renders the chart's clientRbac templates for the installed
// configuration and the given subjects and namespaces.
func GenerateClient(ctx context.Context, rq *ClientRequest) (*ClientOutput, error) {
	if len(rq.Subjects) == 0 {
		return nil, errcat.User.New("at least one --subject is required")
	}
	r := *rq
	r.Namespaces = slices.Compact(slices.Sorted(slices.Values(rq.Namespaces)))
	rq = &r
	installed, source, notes, err := clientValues(ctx, rq)
	if err != nil {
		return nil, err
	}

	v := *installed
	v.ClientRbac.Create = new(true)
	v.ClientRbac.Namespaces = rq.Namespaces
	v.ClientRbac.Subjects = make([]rbac.Subject, len(rq.Subjects))
	for i, s := range rq.Subjects {
		v.ClientRbac.Subjects[i] = s.RBAC()
	}
	render, err := RenderChart(ctx, rq.ManagerNamespace, &v)
	if err != nil {
		return nil, err
	}

	static, scopeNotes, err := checkScope(ctx, rq, installed, render)
	if err != nil {
		return nil, err
	}
	notes = append(notes, scopeNotes...)

	out := &ClientOutput{Header: clientHeader(rq, source, static, notes)}
	if rq.Format == FormatValues {
		out.Body, err = yaml.Marshal(&helm.Values{ClientRbac: helm.ClientRbac{
			Create:     v.ClientRbac.Create,
			Namespaces: v.ClientRbac.Namespaces,
			Subjects:   v.ClientRbac.Subjects,
		}})
	} else {
		out.Body, err = ClientManifests(render, rq.ManagerNamespace)
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// clientValues returns the values the traffic-manager is installed with,
// a description of where they came from, and header notes.
func clientValues(ctx context.Context, rq *ClientRequest) (*helm.Values, string, []string, error) {
	if rq.Values != nil {
		source := rq.ValuesSource
		if source == "" {
			source = "the given values"
		}
		return rq.Values, source, nil, nil
	}
	ns := rq.ManagerNamespace
	askInput := "Pass the values the traffic-manager is installed with as --input."
	if rq.ReleaseLookup == nil {
		return nil, "", nil, errcat.User.New("unable to read the traffic-manager release. " + askInput)
	}
	rel, err := rq.ReleaseLookup(ctx, ns)
	if err != nil {
		if k8serrors.IsForbidden(err) || k8serrors.IsUnauthorized(err) {
			return nil, "", nil, errcat.User.Newf(
				"reading the traffic-manager release in namespace %s needs read access to the Helm release secrets there, and it was refused. %s",
				ns, askInput)
		}
		return nil, "", nil, fmt.Errorf("unable to read the traffic-manager release in namespace %s: %w", ns, err)
	}
	if rel == nil {
		return nil, "", nil, errcat.User.Newf(
			"there's no traffic-manager release in namespace %s. Pass the values it will be installed with as --input.", ns)
	}
	values := &helm.Values{}
	if rel.Config != nil {
		if values, err = helm.ValuesFromMap(rel.Config); err != nil {
			return nil, "", nil, fmt.Errorf("unable to read the values of the traffic-manager release in namespace %s: %w", ns, err)
		}
	}
	var notes []string
	if rel.Chart != nil && rel.Chart.Metadata != nil {
		if rv, cv := strings.TrimPrefix(rel.Chart.Metadata.Version, "v"), version.Structured.String(); rv != cv {
			notes = append(notes, fmt.Sprintf(
				"The release has chart version %s, and the rules come from the chart of this client, version %s.", rv, cv))
		}
	}
	return values, "the values of the traffic-manager release in namespace " + ns, notes, nil
}

// checkScope rejects a requested namespace that the traffic-manager doesn't
// manage. It returns the managed namespaces when the selector is static.
func checkScope(ctx context.Context, rq *ClientRequest, installed *helm.Values, render *Render) ([]string, []string, error) {
	static := staticNamespaces(installed)
	if static != nil {
		for _, ns := range rq.Namespaces {
			if !slices.Contains(static, ns) {
				return nil, nil, errcat.User.Newf("the traffic-manager in namespace %s only manages %s, so it doesn't serve namespace %s",
					rq.ManagerNamespace, strings.Join(static, ", "), ns)
			}
		}
		return static, nil, nil
	}
	if len(rq.Namespaces) == 0 {
		return nil, nil, nil
	}
	sel, err := renderedSelector(render, rq.ManagerNamespace)
	if err != nil {
		return nil, nil, err
	}
	selector, err := meta.LabelSelectorAsSelector(sel)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to parse the traffic-manager's namespace selector: %w", err)
	}
	var notes []string
	for _, ns := range rq.Namespaces {
		nsLabels, ok := namespaceLabels(ctx, rq.Client, ns)
		if !ok {
			if !selector.Matches(labels.Set{nameLabel: ns}) {
				notes = append(notes, fmt.Sprintf(
					"Unable to read the labels of namespace %s, so it's unknown whether the traffic-manager's selector %s matches it.", ns, selector))
			}
			continue
		}
		if !selector.Matches(labels.Set(nsLabels)) {
			return nil, nil, errcat.User.Newf("the traffic-manager in namespace %s selects namespaces with %s, which doesn't match namespace %s",
				rq.ManagerNamespace, selector, ns)
		}
	}
	return nil, notes, nil
}

// staticNamespaces returns the namespaces of a traffic-manager whose
// namespaces are fixed by its values, or nil when it selects them
// dynamically. It follows traffic-manager.namespaces in the chart.
func staticNamespaces(v *helm.Values) []string {
	names := v.Namespaces
	if len(names) == 0 {
		names = v.ManagerRbac.Namespaces
	}
	if len(names) > 0 {
		return slices.Compact(slices.Sorted(slices.Values(names)))
	}
	sel := v.NamespaceSelector
	switch {
	case len(sel.MatchExpressions) == 1 && len(sel.MatchLabels) == 0:
		if me := sel.MatchExpressions[0]; me.Key == nameLabel && me.Operator == meta.LabelSelectorOpIn && len(me.Values) > 0 {
			return slices.Compact(slices.Sorted(slices.Values(me.Values)))
		}
	case len(sel.MatchLabels) == 1 && len(sel.MatchExpressions) == 0:
		if n, ok := sel.MatchLabels[nameLabel]; ok {
			return []string{n}
		}
	}
	return nil
}

// renderedSelector returns the namespace selector from the rendered
// traffic-manager ConfigMap.
func renderedSelector(render *Render, managerNamespace string) (*meta.LabelSelector, error) {
	for _, obj := range render.Objects {
		if obj.GetKind() != "ConfigMap" || obj.GetName() != ReleaseName || ObjectNamespace(obj, managerNamespace) != managerNamespace {
			continue
		}
		data, _, _ := unstructured.NestedString(obj.Object, "data", "namespace-selector.yaml")
		sel := &meta.LabelSelector{}
		if err := yaml.Unmarshal([]byte(data), sel); err != nil {
			return nil, fmt.Errorf("unable to parse the traffic-manager's namespace selector: %w", err)
		}
		return sel, nil
	}
	return nil, fmt.Errorf("the chart rendered no %s ConfigMap", ReleaseName)
}

// namespaceLabels returns the labels of namespace ns. A namespace that
// doesn't exist has only its name label. The second result is false when
// the namespace can't be read.
func namespaceLabels(ctx context.Context, client kubernetes.Interface, ns string) (map[string]string, bool) {
	if client == nil {
		return nil, false
	}
	obj, err := client.CoreV1().Namespaces().Get(ctx, ns, meta.GetOptions{})
	switch {
	case err == nil:
		l := make(map[string]string, len(obj.Labels)+1)
		for k, v := range obj.Labels {
			l[k] = v
		}
		l[nameLabel] = ns
		return l, true
	case k8serrors.IsNotFound(err):
		return map[string]string{nameLabel: ns}, true
	default:
		return nil, false
	}
}

func clientHeader(rq *ClientRequest, source string, static []string, notes []string) []string {
	names := make([]string, len(rq.Subjects))
	for i, s := range rq.Subjects {
		names[i] = s.String()
	}
	h := []string{
		fmt.Sprintf("RBAC for using Telepresence %s with the traffic-manager in namespace %s.", version.Version, rq.ManagerNamespace),
		fmt.Sprintf("Generated by telepresence rbac generate client from %s.", source),
		"Subjects: " + strings.Join(names, ", ") + ".",
	}
	switch {
	case len(rq.Namespaces) > 0:
		h = append(h, "Namespaces: "+strings.Join(rq.Namespaces, ", ")+".")
	case static != nil:
		h = append(h, "Namespaces: "+strings.Join(static, ", ")+", which is every namespace the traffic-manager manages.")
	default:
		h = append(h, "The traffic-manager is cluster-wide, so the namespace rules are granted in every namespace.")
	}
	h = append(h, notes...)
	h = append(h, "")
	if rq.Format == FormatValues {
		return append(h,
			"Add this to the values of the traffic-manager release and upgrade it, so that Helm",
			"creates the roles and bindings.",
			"")
	}
	return append(h,
		"These objects aren't managed by Helm, and their names don't collide with the ones the",
		"chart creates for clientRbac. Review the rules before applying them.",
		"")
}

const clientRbacTemplates = "/templates/clientRbac/"

// ClientManifests returns the objects that the clientRbac templates
// rendered, renamed after ClientName and without the chart's labels, as
// sorted RBAC documents with cluster scope first.
func ClientManifests(render *Render, managerNamespace string) ([]byte, error) {
	type doc struct {
		ns, kind, name string
		obj            any
	}
	var docs []doc
	for i, obj := range render.Objects {
		if !strings.Contains(render.Sources[i], clientRbacTemplates) {
			continue
		}
		var typed any
		switch obj.GetKind() {
		case "Role":
			typed = &rbac.Role{}
		case "ClusterRole":
			typed = &rbac.ClusterRole{}
		case "RoleBinding":
			typed = &rbac.RoleBinding{}
		case "ClusterRoleBinding":
			typed = &rbac.ClusterRoleBinding{}
		default:
			return nil, fmt.Errorf("unexpected %s %s in %s", obj.GetKind(), obj.GetName(), render.Sources[i])
		}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, typed); err != nil {
			return nil, fmt.Errorf("unable to decode %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
		var om *meta.ObjectMeta
		switch o := typed.(type) {
		case *rbac.Role:
			om = &o.ObjectMeta
		case *rbac.ClusterRole:
			om = &o.ObjectMeta
		case *rbac.RoleBinding:
			om = &o.ObjectMeta
			o.RoleRef.Name = clientObjectName(o.RoleRef.Name, managerNamespace)
		case *rbac.ClusterRoleBinding:
			om = &o.ObjectMeta
			o.RoleRef.Name = clientObjectName(o.RoleRef.Name, managerNamespace)
		}
		*om = meta.ObjectMeta{Name: clientObjectName(om.Name, managerNamespace), Namespace: om.Namespace}
		docs = append(docs, doc{ns: om.Namespace, kind: obj.GetKind(), name: om.Name, obj: typed})
	}
	sort.Slice(docs, func(i, j int) bool {
		a, b := docs[i], docs[j]
		if a.ns != b.ns {
			return a.ns < b.ns
		}
		if a.name != b.name {
			return a.name < b.name
		}
		return kindOrder(a.kind) < kindOrder(b.kind)
	})

	var buf bytes.Buffer
	for _, d := range docs {
		data, err := yaml.Marshal(d.obj)
		if err != nil {
			return nil, err
		}
		buf.WriteString("---\n")
		buf.Write(data)
	}
	return buf.Bytes(), nil
}

// clientObjectName maps the name of a clientRbac object in the chart to
// its generated name.
func clientObjectName(name, managerNamespace string) string {
	if suffix, ok := strings.CutPrefix(name, ReleaseName+"-"); ok {
		return ClientName(managerNamespace) + "-" + suffix
	}
	return ClientName(managerNamespace)
}

// kindOrder puts a role before its binding.
func kindOrder(kind string) int {
	if strings.HasSuffix(kind, "Binding") {
		return 1
	}
	return 0
}
