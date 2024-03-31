package mutator

import (
	"context"

	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	informerCore "k8s.io/client-go/informers/core/v1"
	v1 "k8s.io/client-go/listers/core/v1"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
)

const (
	tlsCertFile = `tls.crt`
	tlsKeyFile  = `tls.key`
)

type InjectorCertGetter interface {
	LoadCert() (cert, key []byte, err error)
}

type injectorCertLister struct {
	lister     v1.SecretNamespaceLister
	secretName string
}

func (g *injectorCertLister) LoadCert() ([]byte, []byte, error) {
	s, err := g.lister.Get(g.secretName)
	if err != nil {
		return nil, nil, err
	}
	return s.Data[tlsCertFile], s.Data[tlsKeyFile], nil
}

// GetInjectorCertGetter returns the InjectorCertGetter that retrieves the cert and key
// used by the agent injector.
func GetInjectorCertGetter(ctx context.Context) InjectorCertGetter {
	env := managerutil.GetEnv(ctx)
	ns := env.ManagerNamespace
	f := informer.GetFactory(ctx, ns)
	cV1 := informerCore.New(f, ns, func(options *meta.ListOptions) {
		options.FieldSelector = "metadata.name=" + env.AgentInjectorSecret
	})
	cms := cV1.Secrets()
	cms.Informer() // Ensure that the informer is initialized and registered with the factory
	return &injectorCertLister{
		lister:     cms.Lister().Secrets(ns),
		secretName: env.AgentInjectorSecret,
	}
}
