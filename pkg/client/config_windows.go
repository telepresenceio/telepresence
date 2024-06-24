package client

import (
	"errors"
	"fmt"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

type OSSpecificConfig struct {
	Network Network `json:"network,omitempty" yaml:"network,omitempty"`
}

func GetDefaultOSSpecificConfig() OSSpecificConfig {
	return OSSpecificConfig{
		Network: Network{
			DNSWithFallback: defaultDNSWithFallback,
		},
	}
}

// Merge merges this instance with the non-zero values of the given argument. The argument values take priority.
func (c *OSSpecificConfig) Merge(o *OSSpecificConfig) {
	c.Network.merge(&o.Network)
}

type GSCStrategy string

const (
	defaultDNSWithFallback = true

	// defaultVirtualIPSubnet is an IP that, on windows, is built from 16 class C subnets which were chosen randomly,
	// hoping that they don't collide with another subnet.
	defaultVirtualIPSubnet = "211.55.48.0/20"
)

type Network struct {
	DNSWithFallback bool `json:"dnsWithFallback,omitempty" yaml:"dnsWithFallback,omitempty"`
}

func (n *Network) merge(o *Network) {
	if o.DNSWithFallback != defaultDNSWithFallback { //nolint:gosimple // explicit default comparison
		n.DNSWithFallback = o.DNSWithFallback
	}
}

func (n Network) IsZero() bool {
	return n.DNSWithFallback == defaultDNSWithFallback //nolint:gosimple // explicit default comparison
}

func (n *Network) UnmarshalYAML(node *yaml.Node) (err error) {
	if node.Kind != yaml.MappingNode {
		return errors.New(WithLoc("network must be an object", node))
	}
	ms := node.Content
	top := len(ms)
	for i := 0; i < top; i += 2 {
		kv, err := StringKey(ms[i])
		if err != nil {
			return err
		}
		v := ms[i+1]
		switch kv {
		case "dnsWithFallback":
			err = v.Decode(&n.DNSWithFallback)
			if err != nil {
				return err
			}
		case "globalDNSSearchConfigStrategy":
			logrus.Warn(WithLoc(fmt.Sprintf(`deprecated key %q, no longer needed`, kv), ms[i]))
		default:
			logrus.Warn(WithLoc(fmt.Sprintf("unknown key %q", kv), ms[i]))
		}
	}
	return nil
}
