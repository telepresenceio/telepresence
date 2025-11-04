package client

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

// ReloadDaemonLogLevel calls SetLevel with the log level defined
// for the rootDaemon or userDaemon
// depending on the root flag. Assumes that the config has already been reloaded.
func ReloadDaemonLogLevel(c context.Context) {
	newCfg := GetConfig(c)
	var level string
	levels := newCfg.LogLevels()
	switch ProcessName() {
	case RootDaemonName:
		level = levels.RootDaemon.String()
	case UserDaemonName:
		level = levels.UserDaemon.String()
	case KubeAuthDaemonName:
		level = levels.KubeAuthDaemon.String()
	default:
		level = levels.CLI.String()
	}
	log.SetLevel(c, level)
}
