package agentconfig

import (
	"strconv"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// SpecMatchesIntercept answers the question if an InterceptSpec matches the given
// Intercept config. The spec matches if:
//   - its ServiceName is equal to the config's ServiceName
//   - its ServicePortIdentifier is equal to the config's ServicePortName, or can
//     be parsed to an integer equal to the config's ServicePort
func SpecMatchesIntercept(spec *manager.InterceptSpec, ic *Intercept) bool {
	return ic.ServiceName == spec.ServiceName && IsInterceptFor(spec.ServicePortIdentifier, ic)
}

// IsInterceptFor returns true when the given ServicePortIdentifier is equal to the
// config's ServicePortName, or can be parsed to an integer equal to the config's ServicePort
func IsInterceptFor(spi string, ic *Intercept) bool {
	if spi == ic.ServicePortName {
		return true
	}
	pn, err := strconv.Atoi(spi)
	return err == nil && uint16(pn) == ic.ServicePort
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
