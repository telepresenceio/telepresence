package trafficmgr

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func TestManagerInstallID(t *testing.T) {
	const managerNs = "telepresence"
	const nsUID = "2f6c1f0e-9a51-4b8e-9a8b-0c1d2e3f4a5b"

	newSession := func(t *testing.T, managerAddress string, si *manager.SessionInfo) (*session, *fake.Clientset) {
		t.Helper()
		cfg := client.GetDefaultConfig()
		cfg.Cluster().DefaultManagerNamespace = managerNs
		cfg.Cluster().ManagerAddress = managerAddress
		cs := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: managerNs, UID: nsUID}})
		ctx := k8sapi.WithK8sInterface(client.WithConfig(context.Background(), cfg), cs)
		return &session{
			Cluster:     &k8s.Cluster{Kubeconfig: &k8s.Kubeconfig{Context: ctx}},
			sessionInfo: si,
		}, cs
	}

	failOnAPICall := func(t *testing.T, cs *fake.Clientset) {
		t.Helper()
		cs.PrependReactor("*", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
			t.Errorf("unexpected Kubernetes API call: %s %s", action.GetVerb(), action.GetResource().Resource)
			return true, nil, nil
		})
	}

	t.Run("uses the ID reported by the traffic-manager", func(t *testing.T) {
		s, cs := newSession(t, "", &manager.SessionInfo{ManagerInstallId: "from-manager"})
		failOnAPICall(t, cs)
		require.Equal(t, "from-manager", s.managerInstallID())
	})

	t.Run("external manager without reported ID makes no API call", func(t *testing.T) {
		s, cs := newSession(t, "tls://tm.example.com:443", &manager.SessionInfo{})
		failOnAPICall(t, cs)
		require.Equal(t, k8sapi.ZeroNamespaceID, s.managerInstallID())
	})

	t.Run("falls back to the namespace UID for managers that don't report it", func(t *testing.T) {
		s, _ := newSession(t, "", &manager.SessionInfo{})
		require.Equal(t, nsUID, s.managerInstallID())
	})
}
