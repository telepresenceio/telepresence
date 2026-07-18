package setup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/charts"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// clusterScopedKinds are the chart's kinds that never carry a namespace.
var clusterScopedKinds = map[string]bool{ //nolint:gochecknoglobals // constant lookup table
	"Namespace":                      true,
	"ClusterRole":                    true,
	"ClusterRoleBinding":             true,
	"MutatingWebhookConfiguration":   true,
	"ValidatingWebhookConfiguration": true,
	"CustomResourceDefinition":       true,
	"PriorityClass":                  true,
}

// probeRBAC is P1: it renders the embedded chart with the candidate values and
// checks "create" access on every rendered object plus the install-time extras.
// When the cluster-wide render is not fully permitted, it re-renders scoped to
// the manager namespace and checks that instead.
func (p *Prober) probeRBAC(ctx context.Context, nsExists bool) PrivilegeFacts {
	facts := PrivilegeFacts{}

	clusterWide, missing := p.evaluateChartAccess(ctx, nsExists, p.candidateValues())
	facts.ClusterWide = clusterWide
	facts.Missing = missing

	if clusterWide.Verdict == VerdictYes {
		facts.Namespaced = Finding{Verdict: VerdictYes, Evidence: []string{"implied by cluster-wide result"}}
		return facts
	}

	nsValues := chartutil.CoalesceTables(cloneTopLevel(p.candidateValues()), map[string]any{
		"namespaces": []any{p.ManagerNamespace},
	})
	namespaced, missingNs := p.evaluateChartAccess(ctx, nsExists, nsValues)
	facts.Namespaced = namespaced
	facts.MissingNamespaced = missingNs
	return facts
}

// evaluateChartAccess renders the chart with values, builds the resulting
// ResourceAttributes set plus the install-time extras, and issues a
// SelfSubjectAccessReview for each.
func (p *Prober) evaluateChartAccess(ctx context.Context, nsExists bool, values map[string]any) (Finding, []string) {
	chrt, err := loadEmbeddedChart()
	if err != nil {
		return Finding{Verdict: VerdictUnknown, Evidence: []string{err.Error()}}, nil
	}
	manifest, err := renderChart(ctx, chrt, p.ManagerNamespace, values)
	if err != nil {
		return Finding{Verdict: VerdictUnknown, Evidence: []string{err.Error()}}, nil
	}
	objs, err := decodeManifests(manifest)
	if err != nil {
		return Finding{Verdict: VerdictUnknown, Evidence: []string{err.Error()}}, nil
	}

	ras := attributesForObjects(objs, p.ManagerNamespace)
	ras = append(ras, extraChecks(nsExists, values, p.ManagerNamespace)...)
	ras = dedupeAttributes(ras)

	missing, sweepErr := p.sweepAccess(ctx, ras)
	if sweepErr != nil {
		return Finding{Verdict: VerdictUnknown, Evidence: []string{sweepErr.Error()}}, nil
	}
	if len(missing) == 0 {
		return Finding{Verdict: VerdictYes}, nil
	}
	return Finding{Verdict: VerdictNo}, missing
}

// loadEmbeddedChart mirrors loadCoreChart in pkg/client/cli/helm/chart.go, which
// is unexported.
func loadEmbeddedChart() (*chart.Chart, error) {
	var buf bytes.Buffer
	if err := charts.WriteChart(charts.DirTypeTelepresence, &buf, charts.TelepresenceChartName, version.Structured); err != nil {
		return nil, err
	}
	return loader.LoadArchive(&buf)
}

// renderChart renders chrt client-side, with no cluster contact, and returns
// the release manifest concatenated with every hook's manifest.
func renderChart(ctx context.Context, chrt *chart.Chart, namespace string, values map[string]any) (string, error) {
	install := action.NewInstall(&action.Configuration{})
	install.DryRun = true
	install.ClientOnly = true
	install.ReleaseName = "traffic-manager"
	install.Namespace = namespace

	rel, err := install.RunWithContext(ctx, chrt, values)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString(rel.Manifest)
	for _, h := range rel.Hooks {
		sb.WriteString("\n---\n")
		sb.WriteString(h.Manifest)
	}
	return sb.String(), nil
}

// decodeManifests splits a rendered manifest on YAML document separators and
// decodes each non-empty document into an Unstructured object.
func decodeManifests(manifest string) ([]*unstructured.Unstructured, error) {
	reader := yamlutil.NewYAMLReader(bufio.NewReader(strings.NewReader(manifest)))
	var objs []*unstructured.Unstructured
	for {
		doc, err := reader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		doc = bytes.TrimSpace(doc)
		if len(doc) == 0 {
			continue
		}
		obj := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(doc, &obj.Object); err != nil {
			return nil, fmt.Errorf("unable to decode rendered manifest: %w", err)
		}
		if len(obj.Object) == 0 {
			continue
		}
		objs = append(objs, obj)
	}
	return objs, nil
}

// attributesForObjects builds a "create" ResourceAttributes for every object,
// defaulting the namespace of namespaced kinds to managerNamespace when the
// object doesn't carry one of its own. Name is included even though plain RBAC
// ignores resourceNames on create, because it lets a name-scoped admission
// policy distinguish same-typed objects.
func attributesForObjects(objs []*unstructured.Unstructured, managerNamespace string) []*authv1.ResourceAttributes {
	ras := make([]*authv1.ResourceAttributes, 0, len(objs))
	for _, obj := range objs {
		gvk := obj.GroupVersionKind()
		gvr, _ := meta.UnsafeGuessKindToResource(gvk)

		ns := ""
		if !clusterScopedKinds[gvk.Kind] {
			ns = obj.GetNamespace()
			if ns == "" {
				ns = managerNamespace
			}
		}
		ras = append(ras, &authv1.ResourceAttributes{
			Verb:      "create",
			Group:     gvr.Group,
			Version:   gvr.Version,
			Resource:  gvr.Resource,
			Namespace: ns,
			Name:      obj.GetName(),
		})
	}
	return ras
}

// extraChecks are the install-time RBAC needs that no rendered object implies:
// namespace creation, the helm storage driver's secrets, and the privileged
// PSS label patch that node-agent mode requires.
func extraChecks(nsExists bool, values map[string]any, managerNamespace string) []*authv1.ResourceAttributes {
	var extra []*authv1.ResourceAttributes
	if !nsExists {
		extra = append(extra, &authv1.ResourceAttributes{Verb: "create", Resource: "namespaces"})
	}
	extra = append(extra, &authv1.ResourceAttributes{Verb: "create", Resource: "secrets", Namespace: managerNamespace})
	if nodeAgentEnabled(values) {
		extra = append(extra, &authv1.ResourceAttributes{Verb: "patch", Resource: "namespaces", Name: managerNamespace})
	}
	return extra
}

func nodeAgentEnabled(values map[string]any) bool {
	na, ok := values["nodeAgent"].(map[string]any)
	if !ok {
		return false
	}
	enabled, _ := na["enabled"].(bool)
	return enabled
}

// dedupeAttributes removes ResourceAttributes that are identical in every
// field that a SelfSubjectAccessReview compares.
func dedupeAttributes(ras []*authv1.ResourceAttributes) []*authv1.ResourceAttributes {
	seen := make(map[string]bool, len(ras))
	out := make([]*authv1.ResourceAttributes, 0, len(ras))
	for _, ra := range ras {
		key := strings.Join([]string{ra.Verb, ra.Group, ra.Version, ra.Resource, ra.Subresource, ra.Namespace, ra.Name}, "|")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ra)
	}
	return out
}

// sweepAccess issues one SelfSubjectAccessReview per attribute set and returns
// the itemized, sorted list of denials. It errors only when a review call
// itself fails (transport or API error), never on a denial.
func (p *Prober) sweepAccess(ctx context.Context, ras []*authv1.ResourceAttributes) ([]string, error) {
	sar := p.KubeClient.AuthorizationV1().SelfSubjectAccessReviews()
	var missing []string
	var callErrs []string
	for _, ra := range ras {
		review := &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: ra}}
		result, err := sar.Create(ctx, review, metav1.CreateOptions{})
		if err != nil {
			callErrs = append(callErrs, err.Error())
			continue
		}
		if !result.Status.Allowed {
			missing = append(missing, formatAttributes(ra))
		}
	}
	if len(callErrs) > 0 {
		return missing, errors.New(strings.Join(callErrs, "; "))
	}
	sort.Strings(missing)
	return missing, nil
}

// singleAccessCheck issues one SelfSubjectAccessReview and reports the result
// as a Finding.
func (p *Prober) singleAccessCheck(ctx context.Context, ra *authv1.ResourceAttributes) Finding {
	sar := p.KubeClient.AuthorizationV1().SelfSubjectAccessReviews()
	review := &authv1.SelfSubjectAccessReview{Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: ra}}
	result, err := sar.Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return Finding{Verdict: VerdictUnknown, Evidence: []string{err.Error()}}
	}
	if result.Status.Allowed {
		return Finding{Verdict: VerdictYes}
	}
	return Finding{Verdict: VerdictNo, Evidence: []string{formatAttributes(ra)}}
}

// formatAttributes renders a denial like "create deployments.apps in namespace
// ambassador" or "create clusterroles.rbac.authorization.k8s.io".
func formatAttributes(ra *authv1.ResourceAttributes) string {
	res := ra.Resource
	if ra.Group != "" {
		res += "." + ra.Group
	}
	s := ra.Verb + " " + res
	if ra.Namespace != "" {
		s += " in namespace " + ra.Namespace
	}
	return s
}

func cloneTopLevel(v map[string]any) map[string]any {
	out := make(map[string]any, len(v))
	maps.Copy(out, v)
	return out
}
