package client

import "context"

func MergeAndReplace(c context.Context, defaults *Config, priority *Config) {
	if defaults == nil {
		c := GetDefaultConfig()
		defaults = &c
	}
	defaults.Merge(priority)
	ReplaceConfig(c, defaults)
}
