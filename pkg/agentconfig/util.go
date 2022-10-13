package agentconfig

import (
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// SpecMatchesIntercept answers the question if an InterceptSpec matches the given
// Intercept config. The spec matches if:
//   - its ServiceName is equal to the config's ServiceName
//   - its PortIdentifier is equal to the config's ServicePortName, or can
//     be parsed to an integer equal to the config's ServicePort
func SpecMatchesIntercept(spec *manager.InterceptSpec, ic *Intercept) bool {
	return ic.ServiceName == spec.ServiceName && IsInterceptFor(PortIdentifier(spec.ServicePortIdentifier), ic)
}

// IsInterceptFor returns true when the given PortIdentifier is equal to the
// config's ServicePortName, or can be parsed to an integer equal to the config's ServicePort.
func IsInterceptFor(spi PortIdentifier, ic *Intercept) bool {
	proto, name, num := spi.ProtoAndNameOrNumber()
	if spi.HasProto() && proto != ic.Protocol {
		return false
	}
	if name == "" {
		return num == ic.ServicePort
	}
	return name == ic.ServicePortName
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
