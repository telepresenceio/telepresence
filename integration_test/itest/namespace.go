package itest

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
)

type NamespacePair interface {
	Harness
	ApplyApp(ctx context.Context, name, workload string)
	ApplyEchoService(ctx context.Context, name string, port int)
	ApplyTemplate(ctx context.Context, path string, values any)
	DeleteTemplate(ctx context.Context, path string, values any)
	AppNamespace() string
	TelepresenceConnect(ctx context.Context, args ...string) string
	DeleteSvcAndWorkload(ctx context.Context, workload, name string)
	Kubectl(ctx context.Context, args ...string) error
	KubectlOk(ctx context.Context, args ...string) string
	KubectlOut(ctx context.Context, args ...string) (string, error)
	ManagerNamespace() string
	RollbackTM(ctx context.Context)
	RolloutStatusWait(ctx context.Context, workload string) error
}

type Namespaces struct {
	Namespace         string   `json:"namespace,omitempty"`
	ManagedNamespaces []string `json:"managedNamespaces,omitempty"`
}

func (n *Namespaces) HelmString() string {
	var sb strings.Builder
	sb.WriteByte('{')
	sb.WriteString(n.Namespace)
	for _, m := range n.ManagedNamespaces {
		if m != n.Namespace {
			sb.WriteByte(',')
			sb.WriteString(m)
		}
	}
	sb.WriteByte('}')
	return sb.String()
}

func (n *Namespaces) UniqueList() []string {
	ul := make([]string, 0, len(n.ManagedNamespaces)+1)
	ul = append(ul, n.Namespace)
	for _, m := range n.ManagedNamespaces {
		if m != n.Namespace {
			ul = append(ul, m)
		}
	}
	return ul
}

type namespacesContextKey struct{}

func WithNamespaces(ctx context.Context, namespaces *Namespaces) context.Context {
	return context.WithValue(ctx, namespacesContextKey{}, namespaces)
}

func GetNamespaces(ctx context.Context) *Namespaces {
	if namespaces, ok := ctx.Value(namespacesContextKey{}).(*Namespaces); ok {
		return namespaces
	}
	return nil
}

// The namespaceSuite has no tests. It's sole purpose is to create and destroy the namespaces and
// any non-namespaced resources that we, ourselves, make nsPair specific, such as the
// mutating webhook configuration for the traffic-agent injection.
type nsPair struct {
	Harness
	Namespaces
}

// TelepresenceConnect connects using the AppNamespace and ManagerNamespace.
func (s *nsPair) TelepresenceConnect(ctx context.Context, args ...string) string {
	return TelepresenceOk(ctx,
		append(
			[]string{"connect", "--namespace", s.AppNamespace(), "--manager-namespace", s.ManagerNamespace()},
			args...)...)
}

func WithNamespacePair(ctx context.Context, suffix string, f func(NamespacePair)) {
	s := &nsPair{}
	var namespace string
	namespace, s.Namespace = AppAndMgrNSName(suffix)
	s.ManagedNamespaces = []string{namespace}
	getT(ctx).Run(fmt.Sprintf("Test_Namespaces_%s", suffix), func(t *testing.T) {
		ctx = WithT(ctx, t)
		ctx = WithUser(ctx, s.Namespace+":"+TestUser)
		ctx = WithNamespaces(ctx, &s.Namespaces)
		s.Harness = NewContextHarness(ctx)
		s.PushHarness(ctx, s.setup, s.tearDown)
		defer s.PopHarness()
		f(s)
	})
}

const purposeLabel = "tp-cli-testing"

func (s *nsPair) setup(ctx context.Context) bool {
	CreateNamespaces(ctx, s.AppNamespace(), s.Namespace)
	t := getT(ctx)
	if t.Failed() {
		return false
	}
	err := Kubectl(ctx, s.Namespace, "apply", "-f", filepath.Join(GetOSSRoot(ctx), "testdata", "k8s", "client_sa.yaml"))
	assert.NoError(t, err, "failed to create connect ServiceAccount")
	return !t.Failed()
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
		DeleteNamespaces(ctx, s.AppNamespace(), s.Namespace)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = Kubectl(ctx, "", "delete", "--wait=false", "mutatingwebhookconfiguration", "agent-injector-webhook-"+s.Namespace)
	}()
	wg.Wait()
}

func (s *nsPair) RollbackTM(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	err := Command(ctx, "helm", "rollback", "--no-hooks", "--wait", "--namespace", s.ManagerNamespace(), "traffic-manager").Run()
	t := getT(ctx)
	require.NoError(t, err)
	require.NoError(t, RolloutStatusWait(ctx, s.Namespace, "deploy/traffic-manager"))
	s.CapturePodLogs(ctx, "traffic-manager", "", s.Namespace)
}

func (s *nsPair) AppNamespace() string {
	return s.ManagedNamespaces[0]
}

func (s *nsPair) ManagerNamespace() string {
	return s.Namespace
}

func (s *nsPair) ApplyEchoService(ctx context.Context, name string, port int) {
	getT(ctx).Helper()
	ApplyEchoService(ctx, name, s.AppNamespace(), port)
}

// ApplyApp calls kubectl apply -n <namespace> -f on the given app + .yaml found in testdata/k8s relative
// to the directory returned by GetCurrentDirectory.
func (s *nsPair) ApplyApp(ctx context.Context, name, workload string) {
	getT(ctx).Helper()
	ApplyApp(ctx, name, s.AppNamespace(), workload)
}

func (s *nsPair) RolloutStatusWait(ctx context.Context, workload string) error {
	return RolloutStatusWait(ctx, s.AppNamespace(), workload)
}

func (s *nsPair) DeleteSvcAndWorkload(ctx context.Context, workload, name string) {
	getT(ctx).Helper()
	DeleteSvcAndWorkload(ctx, workload, name, s.AppNamespace())
}

func (s *nsPair) ApplyTemplate(ctx context.Context, path string, values any) {
	s.doWithTemplate(ctx, "apply", path, values)
}

func (s *nsPair) DeleteTemplate(ctx context.Context, path string, values any) {
	yml, err := ReadTemplate(ctx, path, values)
	require.NoError(getT(ctx), err)
	if err = s.Kubectl(dos.WithStdin(ctx, bytes.NewReader(yml)), "apply", "-f", "-"); err != nil {
		dlog.Errorf(ctx, "unable to apply %q", string(yml))
		getT(ctx).Fatal(err)
	}
}

func (s *nsPair) doWithTemplate(ctx context.Context, action, path string, values any) {
	yml, err := ReadTemplate(ctx, path, values)
	require.NoError(getT(ctx), err)
	if err = s.Kubectl(dos.WithStdin(ctx, bytes.NewReader(yml)), action, "-f", "-"); err != nil {
		dlog.Errorf(ctx, "unable to %s %q", action, string(yml))
		getT(ctx).Fatal(err)
	}
}

// Kubectl runs kubectl with the default context and the application namespace.
func (s *nsPair) Kubectl(ctx context.Context, args ...string) error {
	getT(ctx).Helper()
	return Kubectl(ctx, s.AppNamespace(), args...)
}

// KubectlOk runs kubectl with the default context and the application namespace and returns its combined output
// and fails if an error occurred.
func (s *nsPair) KubectlOk(ctx context.Context, args ...string) string {
	out, err := KubectlOut(ctx, s.AppNamespace(), args...)
	require.NoError(getT(ctx), err)
	return out
}

// KubectlOut runs kubectl with the default context and the application namespace and returns its combined output.
func (s *nsPair) KubectlOut(ctx context.Context, args ...string) (string, error) {
	getT(ctx).Helper()
	return KubectlOut(ctx, s.AppNamespace(), args...)
}
