package setup

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/routing"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func TestGatherFacts_Smoke(t *testing.T) {
	client := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ambassador"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "node-1",
				Labels: map[string]string{"kubernetes.io/os": "linux"},
			},
			Spec: corev1.NodeSpec{ProviderID: "kind://docker/kind/kind-control-plane", PodCIDR: "10.244.0.0/16"},
			Status: corev1.NodeStatus{
				Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "172.18.0.2"}},
				NodeInfo:  corev1.NodeSystemInfo{ContainerRuntimeVersion: "containerd://1.6.6"},
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "traffic-manager-quic", Namespace: "ambassador"},
			Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP},
		},
	)
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(*authv1.ResourceAttributes) bool { return true })

	stubRelease := &release.Release{
		Name:      "traffic-manager",
		Namespace: "ambassador",
		Chart:     &chart.Chart{Metadata: &chart.Metadata{Version: "2.31.0"}},
		Config:    map[string]any{"replicaCount": float64(1)},
	}

	srv := newUpdateServer(t, version.Structured.String()+"\n", http.StatusOK)
	p := &Prober{
		KubeClient:       client,
		ManagerNamespace: "ambassador",
		UpdateCheckHost:  srv.URL,
		ReleaseLookup: func(context.Context, string) (*release.Release, error) {
			return stubRelease, nil
		},
		RouteSource: func(context.Context) ([]*routing.Route, error) {
			return []*routing.Route{localRoute("192.168.1.0/24", "eth0")}, nil
		},
	}

	facts, err := p.GatherFacts(context.Background())
	require.NoError(t, err)
	require.NotNil(t, facts)

	assert.Equal(t, "ambassador", facts.ManagerNamespace)
	assert.True(t, facts.NamespaceExists)

	assert.Equal(t, VerdictYes, facts.Privileges.ClusterWide.Verdict)
	assert.Equal(t, "kind", facts.Quic.Provider)
	assert.NotEqual(t, VerdictUnknown, facts.Quic.NodePort.Verdict)
	assert.Equal(t, 1, facts.NodeAgent.TotalNodes)
	assert.Equal(t, 1, facts.NodeAgent.LinuxNodes)
	assert.Equal(t, VerdictYes, facts.Webhook.CanCreate.Verdict)
	assert.Equal(t, 2, facts.Namespaces.Count)
	assert.True(t, facts.Release.Installed)
	require.NotNil(t, facts.Health, "an installed release must produce health facts")
	assert.Equal(t, VerdictNo, facts.Health.VersionSkew.Verdict, "the stub release is newer than the test client")

	assert.Equal(t, "2.31.0", facts.Release.Version)
	assert.Equal(t, VerdictYes, facts.Routing.Summary.Verdict)

	p.ReleaseLookup = nil
	facts, err = p.GatherFacts(context.Background())
	require.NoError(t, err)
	assert.Nil(t, facts.Health, "no installed release must leave the health facts nil")
}
