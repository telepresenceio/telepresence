package informer

import (
	"k8s.io/client-go/informers"

	argorolloutsinformer "github.com/datawire/argo-rollouts-go-client/pkg/client/informers/externalversions"
)

type GlobalFactory interface {
	GetK8sInformerFactory() informers.SharedInformerFactory
	GetArgoRolloutsInformerFactory() argorolloutsinformer.SharedInformerFactory
}

func NewDefaultGlobalFactory(k8s informers.SharedInformerFactory, argoRollouts argorolloutsinformer.SharedInformerFactory) GlobalFactory {
	return &defaultGlobalFactory{k8s: k8s, argoRollouts: argoRollouts}
}

type defaultGlobalFactory struct {
	k8s          informers.SharedInformerFactory
	argoRollouts argorolloutsinformer.SharedInformerFactory
}

func (f *defaultGlobalFactory) GetK8sInformerFactory() informers.SharedInformerFactory {
	return f.k8s
}

func (f *defaultGlobalFactory) GetArgoRolloutsInformerFactory() argorolloutsinformer.SharedInformerFactory {
	return f.argoRollouts
}
