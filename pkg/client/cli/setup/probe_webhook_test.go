package setup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func TestProbeWebhook_Allowed(t *testing.T) {
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(*authv1.ResourceAttributes) bool { return true })

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeWebhook(context.Background(), "unknown")
	require.Equal(t, VerdictYes, facts.CanCreate.Verdict)
	assert.Empty(t, facts.ReachabilityConcern)
}

func TestProbeWebhook_Denied(t *testing.T) {
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, nil) // deny everything

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeWebhook(context.Background(), "unknown")
	require.Equal(t, VerdictNo, facts.CanCreate.Verdict)
}

func TestProbeWebhook_EKSNonVPCCNI(t *testing.T) {
	client := fake.NewClientset(
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "calico-node", Namespace: "kube-system"}},
	)
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(*authv1.ResourceAttributes) bool { return true })

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeWebhook(context.Background(), "eks")
	assert.Contains(t, facts.ReachabilityConcern, "non-VPC CNI")
}

func TestProbeWebhook_EKSWithVPCCNI(t *testing.T) {
	client := fake.NewClientset(
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "calico-node", Namespace: "kube-system"}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "aws-node", Namespace: "kube-system"}},
	)
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(*authv1.ResourceAttributes) bool { return true })

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeWebhook(context.Background(), "eks")
	assert.Empty(t, facts.ReachabilityConcern)
}
