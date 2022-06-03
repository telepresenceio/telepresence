package agentconfig

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// InjectPolicy specifies when the agent injector mutating webhook will inject a traffic-agent into
// a pod.
type InjectPolicy int

var epNames = [...]string{"OnDemand", "OnDemandWhenEnabled", "WhenEnabled"}

const (
	// OnDemand tells the injector to inject the traffic-agent the first time someone tries to intercept the workload
	// unless the annotation telepresence.getambassador.io/inject-traffic-agent annotation is explicitly
	// set to "disabled". A missing annotation means "enabled".
	OnDemand InjectPolicy = iota

	// OnDemandWhenEnabled tells the injector to inject the traffic-agent the first time someone tries to intercept
	// the workload, but only if the telepresence.getambassador.io/inject-traffic-agent annotation is set to "enabled".
	// A missing annotation means "disabled".
	OnDemandWhenEnabled

	// WhenEnabled tells the injector to inject the traffic-agent into all pods that are created or updated when the
	// telepresence.getambassador.io/inject-traffic-agent annotation is set to "enabled". A missing annotation means
	// "disabled".
	WhenEnabled
)

func (aps InjectPolicy) String() string {
	return epNames[aps]
}

func NewEnablePolicy(s string) (InjectPolicy, error) {
	for i, n := range epNames {
		if s == n {
			return InjectPolicy(i), nil
		}
	}
	return 0, fmt.Errorf("invalid InjectPolicy: %q", s)
}

func (aps InjectPolicy) MarshalYAML() (interface{}, error) {
	return aps.String(), nil
}

func (aps *InjectPolicy) EnvDecode(val string) (err error) {
	var as InjectPolicy
	if val == "" {
		as = OnDemand
	} else if as, err = NewEnablePolicy(val); err != nil {
		return err
	}
	*aps = as
	return nil
}

func (aps *InjectPolicy) UnmarshalYAML(node *yaml.Node) (err error) {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	return aps.EnvDecode(s)
}
