package logging

import (
	"context"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

// ReloadDaemonConfig replaces the current config with one loaded from disk and
// calls SetLevel with the log level defined for the rootDaemon or userDaemon
// depending on the root flag
func ReloadDaemonConfig(c context.Context, root bool) error {
	newCfg, err := client.LoadConfig(c)
	if err != nil {
		return err
	}
	client.ReplaceConfig(c, newCfg)
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
