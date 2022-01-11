package userd_k8s

import (
	"context"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// AddLocalOnlyIntercept adds a local-only intercept
func (kc *Cluster) AddLocalOnlyIntercept(c context.Context, spec *manager.InterceptSpec) (*rpc.InterceptResult, error) {
	kc.accLock.Lock()
	if kc.localInterceptedNamespaces == nil {
		kc.localInterceptedNamespaces = map[string]struct{}{}
	}
	kc.LocalIntercepts[spec.Name] = spec.Namespace
	_, found := kc.interceptedNamespaces[spec.Namespace]
	if !found {
		_, found = kc.localInterceptedNamespaces[spec.Namespace]
	}
	kc.localInterceptedNamespaces[spec.Namespace] = struct{}{}
	kc.accLock.Unlock()
	if !found {
		kc.updateDaemonNamespaces(c)
	}
	return &rpc.InterceptResult{
		InterceptInfo: &manager.InterceptInfo{
			Spec:              spec,
			Disposition:       manager.InterceptDispositionType_ACTIVE,
			MechanismArgsDesc: "as local-only",
		},
	}, nil
}

func (kc *Cluster) RemoveLocalOnlyIntercept(c context.Context, name, namespace string) error {
	dlog.Debugf(c, "removing local-only intercept %s", name)
	delete(kc.LocalIntercepts, name)
	for _, otherNs := range kc.LocalIntercepts {
		if otherNs == namespace {
			return nil
		}
	}

	// Ensure that namespace is removed from localInterceptedNamespaces if this was the last local intercept
	// for the given namespace.
	kc.accLock.Lock()
	delete(kc.localInterceptedNamespaces, namespace)
	kc.accLock.Unlock()
	kc.updateDaemonNamespaces(c)
	return nil
}
