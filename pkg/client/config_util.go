package client

import (
	"context"
	"log/slog"

	"github.com/telepresenceio/clog"
)

// ReloadLogLevel calls SetLevel with the log level defined for the current process.
// Assumes that the config has already been reloaded.
func ReloadLogLevel(c context.Context) {
	newCfg := GetConfig(c)
	var level slog.Level
	levels := newCfg.LogLevels()
	switch ProcessName() {
	case RootDaemonName:
		level = levels.RootDaemon
	case UserDaemonName:
		level = levels.UserDaemon
	case KubeAuthDaemonName:
		level = levels.KubeAuthDaemon
	default:
		level = levels.CLI
	}
	if clog.SetTreeLevel(c, level) {
		clog.Infof(c, "Logging at this level %q", clog.LevelWithTrace(level))
	}
}
