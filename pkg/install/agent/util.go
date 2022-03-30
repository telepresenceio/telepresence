package agent

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
