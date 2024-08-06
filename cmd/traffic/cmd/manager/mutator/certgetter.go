package mutator

import (
	"context"
	"os"
	"path/filepath"
	"strings"

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

// GetInjectorCertGetter returns the InjectorCertGetter that retrieves the cert and key
// used by the agent injector.
func GetInjectorCertGetter(ctx context.Context) (icg InjectorCertGetter) {
	env := managerutil.GetEnv(ctx)
	sn := env.AgentInjectorSecret
	if strings.HasPrefix(sn, "/") {
		// Secret is mounted so read certs from there
		icg = getInjectorCertReader(sn)
	} else {
		// Watch Secret that contains the certs
		icg = getInjectorCertLister(ctx, env.ManagerNamespace, sn)
	}
	return icg
}

type injectorCertReader struct {
	certPath string
	keyPath  string
}

func getInjectorCertReader(path string) InjectorCertGetter {
	return &injectorCertReader{
		certPath: filepath.Join(path, tlsCertFile),
		keyPath:  filepath.Join(path, tlsKeyFile),
	}
}

func (g *injectorCertReader) LoadCert() (crt, key []byte, err error) {
	crt, err = os.ReadFile(g.certPath)
	if err != nil {
		return nil, nil, err
	}
	key, err = os.ReadFile(g.keyPath)
	if err != nil {
		return nil, nil, err
	}
	return crt, key, nil
}

type injectorCertLister struct {
	lister     v1.SecretNamespaceLister
	secretName string
}

func getInjectorCertLister(ctx context.Context, namespace, secretName string) InjectorCertGetter {
	f := informer.GetK8sFactory(ctx, namespace)
	cV1 := informerCore.New(f, namespace, func(options *meta.ListOptions) {
		options.FieldSelector = "metadata.name=" + secretName
	})
	cms := cV1.Secrets()
	cms.Informer() // Ensure that the informer is initialized and registered with the factory
	return &injectorCertLister{
		lister:     cms.Lister().Secrets(namespace),
		secretName: secretName,
	}
}

func (g *injectorCertLister) LoadCert() ([]byte, []byte, error) {
	s, err := g.lister.Get(g.secretName)
	if err != nil {
		return nil, nil, err
	}
	return s.Data[tlsCertFile], s.Data[tlsKeyFile], nil
}
