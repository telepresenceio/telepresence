package k8sapi

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// AppProtocolStrategy specifies how the application protocol for a service port is determined
// in case the service.spec.ports.appProtocol is not set.
type AppProtocolStrategy int

var apsNames = [...]string{"http2Probe", "portName", "http", "http2"} //nolint:gochecknoglobals // constant names

const (
	// Http2Probe means never guess. Choose HTTP/1.1 or HTTP/2 by probing (this is the default behavior).
	Http2Probe AppProtocolStrategy = iota

	// PortName means trust educated guess based on port name when appProtocol is missing and perform a http2 probe
	// if no such guess can be made.
	PortName

	// Http means just assume HTTP/1.1.
	Http

	// Http2 means just assume HTTP/2.
	Http2
)

func (aps AppProtocolStrategy) String() string {
	return apsNames[aps]
}

func NewAppProtocolStrategy(s string) (AppProtocolStrategy, error) {
	for i, n := range apsNames {
		if s == n {
			return AppProtocolStrategy(i), nil
		}
	}
	return 0, fmt.Errorf("invalid AppProtcolStrategy: %q", s)
}

func (aps AppProtocolStrategy) MarshalYAML() (any, error) {
	return aps.String(), nil
}

func (aps *AppProtocolStrategy) EnvDecode(val string) (err error) {
	var as AppProtocolStrategy
	if val == "" {
		as = Http2Probe
	} else if as, err = NewAppProtocolStrategy(val); err != nil {
		return err
	}
	*aps = as
	return nil
}

func (aps *AppProtocolStrategy) UnmarshalYAML(node *yaml.Node) (err error) {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	return aps.EnvDecode(s)
}
