package client

import "net/netip"

const defaultAddHostGateway = false

type OSSpecificConfig struct {
	Network Network `json:"network,omitzero"`
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
)

// defaultVirtualSubnet is an IP that, on windows, is built from 16 class C subnets which were chosen randomly,
// hoping that they don't collide with another subnet.
var defaultVirtualSubnet = netip.MustParsePrefix("211.55.48.0/20") //nolint:gochecknoglobals // constant

type Network struct {
	DNSWithFallback bool `json:"dnsWithFallback,omitempty"`
}

func (n *Network) merge(o *Network) {
	if o.DNSWithFallback != defaultDNSWithFallback { //nolint:staticcheck // keep for the semantic clarity
		n.DNSWithFallback = o.DNSWithFallback
	}
}

func (n *Network) IsZero() bool {
	return n == nil || n.DNSWithFallback == defaultDNSWithFallback //nolint:staticcheck // keep for the semantic clarity
}
