package k8sapi

import (
	"context"

	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"

	argoRollouts "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned"
)

func WithJoinedClientSetInterface(ctx context.Context, ki kubernetes.Interface, ari argoRollouts.Interface) context.Context {
	return WithArgoRolloutsInterface(WithK8sInterface(ctx, ki), ari)
}

func GetJoinedClientSetInterface(ctx context.Context) JoinedClientSetInterface {
	return &joinedClientSetInterface{
		GetK8sInterface(ctx),
		GetArgoRolloutsInterface(ctx),
	}
}

func WithArgoRolloutsInterface(ctx context.Context, ari argoRollouts.Interface) context.Context {
	return context.WithValue(ctx, ariKey{}, ari)
}

func WithK8sInterface(ctx context.Context, ki kubernetes.Interface) context.Context {
	return context.WithValue(ctx, kiKey{}, ki)
}

func GetArgoRolloutsInterface(ctx context.Context) argoRollouts.Interface {
	ari, ok := ctx.Value(ariKey{}).(argoRollouts.Interface)
	if !ok {
		return nil
	}
	return ari
}

func GetK8sInterface(ctx context.Context) kubernetes.Interface {
	ki, ok := ctx.Value(kiKey{}).(kubernetes.Interface)
	if !ok {
		panic("K8sInterface requested from a context that has none")
	}
	return ki
}

type kiKey struct{}

type ariKey struct{}

func listOptions(labelSelector labels.Set) meta.ListOptions {
	opts := meta.ListOptions{}
	if len(labelSelector) > 0 {
		opts.LabelSelector = labels.SelectorFromSet(labelSelector).String()
	}
	return opts
}
