package setup

import (
	"context"
	"fmt"

	auth "k8s.io/api/authorization/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/rbacgen"
)

// probeRBAC is P1: it renders the embedded chart with the candidate values and
// checks "create" access on every rendered object plus the install-time extras.
// When the cluster-wide render is not fully permitted, it re-renders scoped to
// the manager namespace and checks that instead.
func (p *Prober) probeRBAC(ctx context.Context, nsExists bool) PrivilegeFacts {
	facts := PrivilegeFacts{}

	facts.X509KubeSystem = p.singleAccessCheck(ctx, &auth.ResourceAttributes{
		Verb:      "create",
		Group:     "rbac.authorization.k8s.io",
		Resource:  "rolebindings",
		Namespace: "kube-system",
		Name:      fmt.Sprintf("traffic-manager-x509-auth-%s", p.ManagerNamespace),
	})

	facts.CertManagerCertificate = p.singleAccessCheck(ctx, &auth.ResourceAttributes{
		Verb:      "create",
		Group:     "cert-manager.io",
		Resource:  certManagerResource,
		Namespace: p.ManagerNamespace,
	})

	clusterWide, missing, attrs := p.evaluateChartAccess(ctx, nsExists, p.candidateValues())
	facts.ClusterWide = clusterWide
	facts.Missing = missing
	facts.MissingAttributes = attrs

	if clusterWide.Verdict == VerdictYes {
		facts.Namespaced = Finding{Verdict: VerdictYes, Evidence: []string{"implied by cluster-wide result"}}
		return facts
	}

	nsValues := p.candidateValues().DeepCopy()
	nsValues.Namespaces = []string{p.ManagerNamespace}
	namespaced, missingNs, nsAttrs := p.evaluateChartAccess(ctx, nsExists, nsValues)
	facts.Namespaced = namespaced
	facts.MissingNamespaced = missingNs
	facts.MissingNamespacedAttributes = nsAttrs
	return facts
}

// evaluateChartAccess renders the chart with values, builds the resulting
// ResourceAttributes set plus the install-time extras, and issues a
// SelfSubjectAccessReview for each. The formatted denial strings and the
// structured DeniedAttribute list both derive from the same sorted sweep
// result, so they never drift apart.
func (p *Prober) evaluateChartAccess(ctx context.Context, nsExists bool, values *helm.Values) (Finding, []string, []rbacgen.DeniedAttribute) {
	render, err := rbacgen.RenderChart(ctx, p.ManagerNamespace, values)
	if err != nil {
		return Finding{Verdict: VerdictUnknown, Evidence: []string{err.Error()}}, nil, nil
	}

	// setup upgrades as well as installs, so the hook objects are checked too.
	ras := rbacgen.InstallAttributes(render.All(), p.ManagerNamespace, nsExists, values)
	denied, sweepErr := rbacgen.Sweep(ctx, p.KubeClient, ras)
	if sweepErr != nil {
		return Finding{Verdict: VerdictUnknown, Evidence: []string{sweepErr.Error()}}, nil, nil
	}
	if len(denied) == 0 {
		return Finding{Verdict: VerdictYes}, nil, nil
	}
	missing := make([]string, len(denied))
	attrs := make([]rbacgen.DeniedAttribute, len(denied))
	for i, ra := range denied {
		missing[i] = rbacgen.FormatAttributes(ra)
		attrs[i] = rbacgen.ToDeniedAttribute(ra)
	}
	return Finding{Verdict: VerdictNo}, missing, attrs
}

// singleAccessCheck issues one SelfSubjectAccessReview and reports the result
// as a Finding.
func (p *Prober) singleAccessCheck(ctx context.Context, ra *auth.ResourceAttributes) Finding {
	allowed, err := rbacgen.Allowed(ctx, p.KubeClient, ra)
	if err != nil {
		return Finding{Verdict: VerdictUnknown, Evidence: []string{err.Error()}}
	}
	if allowed {
		return Finding{Verdict: VerdictYes}
	}
	return Finding{Verdict: VerdictNo, Evidence: []string{rbacgen.FormatAttributes(ra)}}
}
