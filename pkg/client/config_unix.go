//go:build !windows

package client

// defaultVirtualIPSubnet A randomly chosen class E subnet.
const defaultVirtualIPSubnet = "246.246.0.0/16"

type OSSpecificConfig struct{}

func GetDefaultOSSpecificConfig() OSSpecificConfig {
	return OSSpecificConfig{}
}

func (c *OSSpecificConfig) Merge(o *OSSpecificConfig) {
}
