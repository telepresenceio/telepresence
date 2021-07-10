package userd_k8s

import (
	"context"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// AddLocalOnlyIntercept adds a local-only intercept
func (tm *Cluster) AddLocalOnlyIntercept(c context.Context, spec *manager.InterceptSpec) (*rpc.InterceptResult, error) {
	tm.accLock.Lock()
	if tm.localInterceptedNamespaces == nil {
		tm.localInterceptedNamespaces = map[string]struct{}{}
	}
	tm.LocalIntercepts[spec.Name] = spec.Namespace
	_, found := tm.interceptedNamespaces[spec.Namespace]
	if !found {
		_, found = tm.localInterceptedNamespaces[spec.Namespace]
	}
	tm.localInterceptedNamespaces[spec.Namespace] = struct{}{}
	tm.accLock.Unlock()
	if !found {
		tm.updateDaemonNamespaces(c)
	}
	return &rpc.InterceptResult{
		InterceptInfo: &manager.InterceptInfo{
			Spec:              spec,
			Disposition:       manager.InterceptDispositionType_ACTIVE,
			MechanismArgsDesc: "as local-only",
		},
	}, nil
}

func (tm *Cluster) RemoveLocalOnlyIntercept(c context.Context, name, namespace string) error {
	dlog.Debugf(c, "removing local-only intercept %s", name)
	delete(tm.LocalIntercepts, name)
	for _, otherNs := range tm.LocalIntercepts {
		if otherNs == namespace {
			return nil
		}
	}

	// Ensure that namespace is removed from localInterceptedNamespaces if this was the last local intercept
	// for the given namespace.
	tm.accLock.Lock()
	delete(tm.localInterceptedNamespaces, namespace)
	tm.accLock.Unlock()
	tm.updateDaemonNamespaces(c)
	return nil
}
