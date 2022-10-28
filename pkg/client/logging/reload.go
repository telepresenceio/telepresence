package logging

import (
	"context"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

// ReloadDaemonConfig calls SetLevel with the log level defined
// for the rootDaemon or userDaemon
// depending on the root flag. Assumes that the config has already been reloaded.
func ReloadDaemonConfig(c context.Context, root bool) error {
	newCfg := client.GetConfig(c)
	var level string
	if root {
		level = newCfg.LogLevels.RootDaemon.String()
	} else {
		level = newCfg.LogLevels.UserDaemon.String()
	}
	log.SetLevel(c, level)
	dlog.Info(c, "Configuration reloaded")
	return nil
}
