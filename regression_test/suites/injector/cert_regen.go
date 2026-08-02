package injector

import (
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// injectorTLSSecretName is the chart's default agentInjector.secret.name
// (charts/telepresence-oss/values.yaml); no catalog spec overrides it, so it
// is stable across every manager spec this suite uses.
const injectorTLSSecretName = "mutator-webhook-tls"

// CertRegen proves that forcing the injector's certificate to regenerate
// (agentInjector.certificate.regenerate=true) doesn't break intercepts for
// either way the webhook can access the cert (accessMethod watch/mount),
// and that deleting the live TLS secret doesn't permanently break
// injection: a fresh workload created afterward still gets its agent once
// the webhook recovers. Mirrors integration_test/injector_test.go's
// Test_HelmUpgradeWebhookSecret/Test_HelmUpgradeMountedWebhookSecret, plus
// an explicit secret deletion.
type CertRegen struct {
	rt.Suite
}

func init() {
	rt.Register(&CertRegen{}, rt.InArea("injector"))
}

func (s *CertRegen) Test_AccessMethods() {
	for _, m := range []string{"watch", "mount"} {
		s.Run(m, func() { s.runAccessMethod(m) })
	}
}

func (s *CertRegen) runAccessMethod(accessMethod string) {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	conn := switchManagerSpec(t, ctx, managers.CertRegen(accessMethod), ns)

	wl := rt.Get(t, rt.WorkloadFixture(ns, workloads.Echo("cert-regen-"+accessMethod)))
	ls := s.LocalEcho()
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
	a.Detach(t)

	if _, err := s.R().Kubectl(ctx, managers.ManagerNamespace, "delete", "secret",
		injectorTLSSecretName, "--ignore-not-found"); err != nil {
		t.Fatalf("deleting %s: %v", injectorTLSSecretName, err)
	}

	freshTpl := workloads.Echo("cert-regen-" + accessMethod + "-post")
	freshTpl.Annotations = map[string]string{annotation.InjectTrafficAgent: "enabled"}
	fresh := rt.Get(t, rt.WorkloadFixture(ns, freshTpl))
	s.Eventually(func() bool {
		return hasAgentContainer(ctx, s.R(), fresh.Namespace, fresh.Name)
	}, agentPollTimeout, agentPollInterval,
		"accessMethod %s: a new workload should still get its agent after the injector's TLS secret is deleted",
		accessMethod)
}
