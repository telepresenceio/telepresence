package vif

import (
	"context"
	"log/slog"
	"time"

	"gvisor.dev/gvisor/pkg/log"

	"github.com/telepresenceio/clog"
)

type clogEmitter struct {
	context.Context
}

func (l clogEmitter) Emit(_ int, level log.Level, _ time.Time, format string, v ...interface{}) { //nolint:goprintffuncname // not our API
	switch level {
	case log.Debug:
		clog.Debugf(l, format, v...)
	case log.Info:
		clog.Infof(l, format, v...)
	case log.Warning:
		clog.Warnf(l, format, v...)
	}
}

func InitLogger(ctx context.Context) {
	log.SetTarget(&clogEmitter{Context: ctx})
	var gl log.Level
	switch {
	case clog.Enabled(ctx, slog.LevelDebug):
		gl = log.Debug
	case clog.Enabled(ctx, slog.LevelInfo):
		gl = log.Info
	default:
		gl = log.Warning
	}
	log.SetLevel(gl)
}
