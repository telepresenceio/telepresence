package itest

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type NamespacePair interface {
	Harness
	ApplyApp(ctx context.Context, name, workload string)
	ApplyEchoService(ctx context.Context, name string, port int)
	AppNamespace() string
	DeleteSvcAndWorkload(ctx context.Context, workload, name string)
	Kubectl(ctx context.Context, args ...string) error
	KubectlOut(ctx context.Context, args ...string) (string, error)
	ManagerNamespace() string
	RolloutStatusWait(ctx context.Context, workload string) error
}

// The namespaceSuite has no tests. It's sole purpose is to create and destroy the namespaces and
// any non-namespaced resources that we, ourselves, make nsPair specific, such as the
// mutating webhook configuration for the traffic-agent injection.
type nsPair struct {
	Harness
	namespace        string
	managerNamespace string
}

func WithNamespacePair(ctx context.Context, suffix string, f func(NamespacePair)) {
	s := &nsPair{}
	s.namespace, s.managerNamespace = AppAndMgrNSName(suffix)
	getT(ctx).Run(fmt.Sprintf("Test_Namespaces_%s", suffix), func(t *testing.T) {
		ctx = withT(ctx, t)
		ctx = WithEnv(ctx, map[string]string{"TELEPRESENCE_MANAGER_NAMESPACE": s.managerNamespace})
		s.Harness = NewContextHarness(ctx)
		s.PushHarness(ctx, s.setup, s.tearDown)
		defer s.PopHarness()
		f(s)
	})
}

const purposeLabel = "tp-cli-testing"

func (s *nsPair) setup(ctx context.Context) bool {
	CreateNamespaces(ctx, s.namespace, s.managerNamespace)
	err := Run(WithModuleRoot(ctx), "kubectl", "apply", "-n", s.managerNamespace, "-f", filepath.Join("k8s", "client_connect_rbac.yaml"))
	require.NoError(getT(ctx), err, "failed to create connect Role/RoleBinding", TestUser)
	return true
}

func AppAndMgrNSName(suffix string) (appNS, mgrNS string) {
	mgrNS = fmt.Sprintf("ambassador-%s", suffix)
	appNS = fmt.Sprintf("telepresence-%s", suffix)
	return
}

func (s *nsPair) tearDown(ctx context.Context) {
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		DeleteNamespaces(ctx, s.namespace, s.managerNamespace)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = Kubectl(ctx, "", "delete", "--wait=false", "mutatingwebhookconfiguration", "agent-injector-webhook-"+s.managerNamespace)
	}()
	wg.Wait()
}

func (s *nsPair) AppNamespace() string {
	return s.namespace
}

func (s *nsPair) ManagerNamespace() string {
	return s.managerNamespace
}

func (s *nsPair) ApplyEchoService(ctx context.Context, name string, port int) {
	getT(ctx).Helper()
	ApplyEchoService(ctx, name, s.namespace, port)
}

func (s *nsPair) ApplyApp(ctx context.Context, name, workload string) {
	getT(ctx).Helper()
	ApplyApp(ctx, name, s.namespace, workload)
}

func (s *nsPair) RolloutStatusWait(ctx context.Context, workload string) error {
	return RolloutStatusWait(ctx, s.namespace, workload)
}

func (s *nsPair) DeleteSvcAndWorkload(ctx context.Context, workload, name string) {
	getT(ctx).Helper()
	DeleteSvcAndWorkload(ctx, workload, name, s.namespace)
}

// Kubectl runs kubectl with the default context and the application namespace
func (s *nsPair) Kubectl(ctx context.Context, args ...string) error {
	getT(ctx).Helper()
	return Kubectl(ctx, s.namespace, args...)
}

// KubectlOut runs kubectl with the default context and the application namespace and returns its combined output
func (s *nsPair) KubectlOut(ctx context.Context, args ...string) (string, error) {
	getT(ctx).Helper()
	return KubectlOut(ctx, s.namespace, args...)
}
