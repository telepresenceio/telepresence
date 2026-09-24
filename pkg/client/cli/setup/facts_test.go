package setup

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"

	auth "k8s.io/api/authorization/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/routing"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func TestGatherFacts_Smoke(t *testing.T) {
	client := fake.NewClientset(
		&core.Namespace{ObjectMeta: meta.ObjectMeta{Name: "ambassador"}},
		&core.Namespace{ObjectMeta: meta.ObjectMeta{Name: "default"}},
		&core.Node{
			ObjectMeta: meta.ObjectMeta{
				Name:   "node-1",
				Labels: map[string]string{"kubernetes.io/os": "linux"},
			},
			Spec: core.NodeSpec{ProviderID: "kind://docker/kind/kind-control-plane", PodCIDR: "10.244.0.0/16"},
			Status: core.NodeStatus{
				Addresses: []core.NodeAddress{{Type: core.NodeInternalIP, Address: "172.18.0.2"}},
				NodeInfo:  core.NodeSystemInfo{ContainerRuntimeVersion: "containerd://1.6.6"},
			},
		},
		&core.Service{
			ObjectMeta: meta.ObjectMeta{Name: "traffic-manager-quic", Namespace: "ambassador"},
			Spec:       core.ServiceSpec{Type: core.ServiceTypeClusterIP},
		},
	)
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(*auth.ResourceAttributes) bool { return true })

	releaseConfig, err := (&helm.Values{ReplicaCount: new(int32(1))}).ToMap()
	require.NoError(t, err)
	stubRelease := &release.Release{
		Name:      "traffic-manager",
		Namespace: "ambassador",
		Chart:     &chart.Chart{Metadata: &chart.Metadata{Version: "2.31.0"}},
		Config:    releaseConfig,
	}

	srv := newUpdateServer(t, version.Structured.String()+"\n", http.StatusOK)
	var phases []string
	var outcomePhases []string
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
		Progress: func(phase string) { phases = append(phases, phase) },
		Outcome: func(phase string, verdict Verdict, summary string) {
			outcomePhases = append(outcomePhases, phase)
			assert.NotEmpty(t, verdict, "phase %q must report a verdict", phase)
			assert.NotEmpty(t, summary, "phase %q must report a summary", phase)
		},
	}

	facts, err := p.GatherFacts(context.Background())
	require.NoError(t, err)
	require.NotNil(t, facts)
	assert.Equal(t, ProbePhases, phases, "phase sequence must not depend on whether a release is installed")
	assert.Equal(t, ProbePhases, outcomePhases, "every phase must report exactly one outcome, in order")

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

	phases = nil
	p.ReleaseLookup = nil
	facts, err = p.GatherFacts(context.Background())
	require.NoError(t, err)
	assert.Nil(t, facts.Health, "no installed release must leave the health facts nil")
	assert.Equal(t, ProbePhases, phases, "phase sequence must not depend on whether a release is installed")
}
