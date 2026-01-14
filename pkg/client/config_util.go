package client

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

// ReloadLogLevel calls SetLevel with the log level defined for the current process.
// Assumes that the config has already been reloaded.
func ReloadLogLevel(c context.Context) {
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
