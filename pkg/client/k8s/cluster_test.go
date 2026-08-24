package k8s

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func TestClassifyUnreachable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "dns lookup failure",
			err:  fmt.Errorf(`Get "https://x.example:443/version": dial tcp: %w`, &net.DNSError{Err: "no such host", Name: "x.example", IsNotFound: true}),
			want: "could not be resolved",
		},
		{
			name: "unknown certificate authority",
			err:  fmt.Errorf("tls: %w", x509.UnknownAuthorityError{}),
			want: "TLS certificate could not be verified",
		},
		{
			name: "unauthorized",
			err:  k8serrors.NewUnauthorized("token expired"),
			want: "authentication was rejected",
		},
		{
			name: "forbidden",
			err:  k8serrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", errors.New("nope")),
			want: "access was forbidden",
		},
		{
			name: "connection refused",
			err:  errors.New(`Get "https://x:443/version": dial tcp 10.0.0.1:443: connect: connection refused`),
			want: "refused the connection",
		},
		{
			name: "timeout",
			err:  errors.New(`Get "https://x:443/version": net/http: request canceled (Client.Timeout exceeded while awaiting headers)`),
			want: "timed out",
		},
		{
			name: "no route to host",
			err:  errors.New(`dial tcp 10.0.0.1:443: connect: no route to host`),
			want: "no network route",
		},
		{
			name: "unrecognized falls back to raw error",
			err:  errors.New("something entirely unexpected"),
			want: "something entirely unexpected",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyUnreachable(tt.err)
			if !strings.Contains(got, tt.want) {
				t.Errorf("classifyUnreachable() = %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

func newTestCluster(namespace string, mapped ...string) *Cluster {
	return &Cluster{
		Kubeconfig:       &Kubeconfig{Context: context.Background(), Namespace: namespace},
		MappedNamespaces: mapped,
	}
}

// TestApplyNamespaceList_MarksAccessibleWithoutProbing asserts every reported namespace
// is marked accessible without probing: the Cluster carries no Kubernetes interface, so
// a panic here would mean canAccessNS ran instead of trusting the manager's report.
func TestApplyNamespaceList_MarksAccessibleWithoutProbing(t *testing.T) {
	kc := newTestCluster("default")
	kc.applyNamespaceList(&manager.NamespaceList{Namespaces: []string{"ns-a", "ns-b"}})

	require.True(t, kc.namespacesFromManager)
	require.Equal(t, []string{"ns-a", "ns-b"}, kc.GetCurrentNamespaces(true))
	require.Equal(t, []string{"ns-a", "ns-b"}, kc.GetCurrentNamespaces(false))
}

// TestApplyNamespaceList_FiltersByMappedNamespaces confirms that an explicit
// --mapped-namespaces filter still narrows the manager-reported set, exactly as it narrows
// a Kubernetes-sourced snapshot.
func TestApplyNamespaceList_FiltersByMappedNamespaces(t *testing.T) {
	kc := newTestCluster("default", "ns-a")
	kc.applyNamespaceList(&manager.NamespaceList{Namespaces: []string{"ns-a", "ns-b"}})

	require.Equal(t, []string{"ns-a"}, kc.GetCurrentNamespaces(true))
}

// TestApplyNamespaceList_UpdatesOnSubsequentLists confirms a later list replaces the
// earlier snapshot rather than merging with it, matching live-watch semantics: a namespace
// dropped from the manager's managed set disappears from GetCurrentNamespaces.
func TestApplyNamespaceList_UpdatesOnSubsequentLists(t *testing.T) {
	kc := newTestCluster("default")
	kc.applyNamespaceList(&manager.NamespaceList{Namespaces: []string{"ns-a", "ns-b"}})
	kc.applyNamespaceList(&manager.NamespaceList{Namespaces: []string{"ns-b"}})

	require.Equal(t, []string{"ns-b"}, kc.GetCurrentNamespaces(true))
}

// TestFindManagerServiceNamespace_PrefersDefault: with the manager present in
// both a custom and the default namespace, the default is returned.
func TestFindManagerServiceNamespace_PrefersDefault(t *testing.T) {
	kc := newTestCluster("default")
	kc.Context = k8sapi.WithK8sInterface(kc.Context, fake.NewClientset(
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "traffic-manager", Namespace: "custom"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "traffic-manager", Namespace: defaultManagerNamespace}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "default"}},
	))
	ns, ok := kc.findManagerServiceNamespace()
	require.True(t, ok)
	require.Equal(t, defaultManagerNamespace, ns)
}

// TestFindManagerServiceNamespace_CustomNamespace: with the manager only in a
// custom namespace, that namespace is returned and unrelated services (the
// fake ignores the field selector) are skipped by the name re-check.
func TestFindManagerServiceNamespace_CustomNamespace(t *testing.T) {
	kc := newTestCluster("default")
	kc.Context = k8sapi.WithK8sInterface(kc.Context, fake.NewClientset(
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "default"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "traffic-manager", Namespace: "telepresence"}},
	))
	ns, ok := kc.findManagerServiceNamespace()
	require.True(t, ok)
	require.Equal(t, "telepresence", ns)
}

// TestFindManagerServiceNamespace_NotInstalled: no traffic-manager Service
// anywhere yields no namespace.
func TestFindManagerServiceNamespace_NotInstalled(t *testing.T) {
	kc := newTestCluster("default")
	kc.Context = k8sapi.WithK8sInterface(kc.Context, fake.NewClientset(
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "default"}},
	))
	_, ok := kc.findManagerServiceNamespace()
	require.False(t, ok)
}
