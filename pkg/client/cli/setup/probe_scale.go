package setup

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// probeNamespaceScale is P5: the cluster's namespace count, used as evidence
// for the namespace-scoping interview question, not as a decision threshold.
func (p *Prober) probeNamespaceScale(ctx context.Context) NamespaceFacts {
	list, err := p.KubeClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsForbidden(err) {
			return NamespaceFacts{ListDenied: true}
		}
		return NamespaceFacts{ListError: err.Error()}
	}
	return NamespaceFacts{Count: len(list.Items)}
}
