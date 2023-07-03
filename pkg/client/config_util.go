package client

import (
	"context"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

func MergeAndReplace(c context.Context, defaults Config, priority Config, root bool) error {
	if defaults == nil {
		defaults = GetDefaultConfig()
	}
	defaults.Merge(priority)
	ReplaceConfig(c, defaults)
	return ReloadDaemonConfig(c, root)
}

func RestoreDefaults(c context.Context, root bool) error {
	pri, err := LoadConfig(c)
	if err != nil {
		return err
	}
	return MergeAndReplace(c, nil, pri, root)
}

// ReloadDaemonConfig calls SetLevel with the log level defined
// for the rootDaemon or userDaemon
// depending on the root flag. Assumes that the config has already been reloaded.
func ReloadDaemonConfig(c context.Context, root bool) error {
	newCfg := GetConfig(c)
	var level string
	if root {
		level = newCfg.LogLevels().RootDaemon.String()
	} else {
		level = newCfg.LogLevels().UserDaemon.String()
	}
	log.SetLevel(c, level)
	dlog.Info(c, "Configuration reloaded")
	return nil
}
