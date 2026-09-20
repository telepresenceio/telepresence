package k8sapi

import (
	"k8s.io/client-go/kubernetes"

	argoRollouts "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned"
	typedArgoRollouts "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned/typed/rollouts/v1alpha1"
)

// JoinedClientSetInterface is the Kubernetes clientset extended with the Argo
// Rollouts clientset. The Argo clientset is exposed as a whole rather than
// embedded, because its Discovery method has a different return type.
type JoinedClientSetInterface interface {
	kubernetes.Interface
	ArgoRollouts() argoRollouts.Interface
	ArgoprojV1alpha1() typedArgoRollouts.ArgoprojV1alpha1Interface
}

type joinedClientSetInterface struct {
	kubernetes.Interface
	ari argoRollouts.Interface
}

func (j joinedClientSetInterface) ArgoRollouts() argoRollouts.Interface {
	return j.ari
}

func (j joinedClientSetInterface) ArgoprojV1alpha1() typedArgoRollouts.ArgoprojV1alpha1Interface {
	return j.ari.ArgoprojV1alpha1()
}
