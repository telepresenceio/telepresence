package agentconfig

import (
	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// SpecMatchesIntercept answers the question if an InterceptSpec matches the given
// Intercept config. The spec matches if:
//   - its ServiceName is equal to the config's ServiceName
//   - its PortIdentifier is equal to the config's ServicePortName, or can
//     be parsed to an integer equal to the config's ServicePort
func SpecMatchesIntercept(spec *manager.InterceptSpec, ic *Intercept) bool {
	if spec.ServiceName != "" && spec.ServiceName != ic.ServiceName {
		return false
	}
	if spec.PortIdentifier != "" {
		pi := PortIdentifier(spec.PortIdentifier)
		if spec.ServiceUid != "" {
			return IsInterceptForService(pi, ic)
		}
		return IsInterceptForContainer(pi, ic)
	}
	return uint16(spec.ContainerPort) == ic.ContainerPort && (spec.Protocol == "" || core.Protocol(spec.Protocol) == ic.Protocol)
}

// IsInterceptForService returns true when the given PortIdentifier is equal to the
// config's ServicePortName, or can be parsed to an integer equal to the config's ServicePort.
func IsInterceptForService(pi PortIdentifier, ic *Intercept) bool {
	proto, name, num := pi.ProtoAndNameOrNumber()
	if pi.HasProto() && proto != ic.Protocol {
		return false
	}
	if name == "" {
		return num == ic.ServicePort
	}
	return name == ic.ServicePortName
}

// IsInterceptForContainer returns true when the given PortIdentifier is equal to the
// config's ContainerPort, or can be parsed to an integer equal to the config's ContainerPort.
func IsInterceptForContainer(pi PortIdentifier, ic *Intercept) bool {
	proto, name, num := pi.ProtoAndNameOrNumber()
	if pi.HasProto() && proto != ic.Protocol {
		return false
	}
	if name == "" {
		return num == ic.ContainerPort
	}
	return name == ic.ContainerPortName
}

// PortUniqueIntercepts returns a slice of intercepts for the container where each intercept
// is unique with respect to the AgentPort and Protocol.
// This method should always be used when iterating the intercepts, except for when an
// intercept is identified via a service.
func PortUniqueIntercepts(cn *Container) []*Intercept {
	um := make(map[PortAndProto]struct{}, len(cn.Intercepts))
	ics := make([]*Intercept, 0, len(cn.Intercepts))
	for _, ic := range cn.Intercepts {
		k := PortAndProto{ic.AgentPort, ic.Protocol}
		if _, ok := um[k]; !ok {
			um[k] = struct{}{}
			ics = append(ics, ic)
		}
	}
	return ics
}

// ProxyPort returns a port that can be used as a proxy for container port for the given Intercept.
// The proxy port will be the intercept's agentPort + the maximum number of possible intercepts for the sidecar.
func (s *Sidecar) ProxyPort(ic *Intercept) uint16 {
	return ic.AgentPort + 11 + uint16(s.numberOfPossibleIntercepts())
}

func (s *Sidecar) numberOfPossibleIntercepts() (count int) {
	for _, c := range s.Containers {
		count += len(c.Intercepts)
	}
	return
}
