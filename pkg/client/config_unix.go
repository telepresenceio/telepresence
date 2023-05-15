//go:build !windows

package client

type OSSpecificConfig struct{}

func GetDefaultOSSpecificConfig() OSSpecificConfig {
	return OSSpecificConfig{}
}

func (c *OSSpecificConfig) Merge(o *OSSpecificConfig) {
}
