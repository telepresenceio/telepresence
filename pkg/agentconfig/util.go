package agentconfig

import "github.com/telepresenceio/telepresence/v2/pkg/types"

// IsInterceptForService returns true when the given PortIdentifier is equal to the
// config's ServicePortName, or can be parsed to an integer equal to the config's ServicePort.
func IsInterceptForService(pi types.PortIdentifier, ic *Intercept) bool {
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
func IsInterceptForContainer(pi types.PortIdentifier, ic *Intercept) bool {
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
	um := make(map[types.PortAndProto]struct{}, len(cn.Intercepts))
	ics := make([]*Intercept, 0, len(cn.Intercepts))
	for _, ic := range cn.Intercepts {
		k := types.PortAndProto{Port: ic.AgentPort, Proto: ic.Protocol}
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
