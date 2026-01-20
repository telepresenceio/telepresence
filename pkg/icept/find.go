package icept

import (
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// FindContainer finds the container configuration that matches the given InterceptSpec.
func FindContainer(ac *agentconfig.Sidecar, spec *manager.InterceptSpec) (foundCN *agentconfig.Container, err error) {
	if spec.ContainerName == "" {
		if len(ac.Containers) == 1 {
			return ac.Containers[0], nil
		}
		return nil, errcat.User.Newf("%s %s.%s has more than one container",
			ac.WorkloadKind, ac.WorkloadName, ac.Namespace)
	}
	for _, cn := range ac.Containers {
		if spec.ContainerName == cn.Name {
			return cn, nil
		}
	}
	return nil, errcat.User.Newf("%s %s.%s has no container named %s",
		ac.WorkloadKind, ac.WorkloadName, ac.Namespace, spec.ContainerName)
}

// FindIntercept finds the intercept configuration that matches either the given InterceptSpec's service/service port or a container port in case
// the InterceptSpec targets a headless service.
func FindIntercept(ac *agentconfig.Sidecar, spec *manager.InterceptSpec) (foundCN *agentconfig.Container, foundIC *agentconfig.Intercept, err error) {
	return ac.FindIntercept(spec.ServiceName, spec.ContainerName, types.PortIdentifier(spec.PortIdentifier))
}

// FindContainerIntercept finds the intercept configuration that matches the given port identifier.
func FindContainerIntercept(ac *agentconfig.Sidecar, cn *agentconfig.Container, pi types.PortIdentifier) (*agentconfig.Intercept, error) {
	for _, ic := range cn.Intercepts {
		if agentconfig.IsInterceptForContainer(pi, ic) {
			return ic, nil
		}
	}
	return nil, errcat.User.Newf("%s %s.%s, container %s has no port matching %s", ac.WorkloadKind, ac.WorkloadName, ac.Namespace, cn.Name, pi)
}
