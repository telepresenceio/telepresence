package client

import (
	"context"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

// ReloadDaemonLogLevel calls SetLevel with the log level defined
// for the rootDaemon or userDaemon
// depending on the root flag. Assumes that the config has already been reloaded.
func ReloadDaemonLogLevel(c context.Context, root bool) error {
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
