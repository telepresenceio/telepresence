package vif

import (
	"context"
	"time"

	"gvisor.dev/gvisor/pkg/log"

	"github.com/datawire/dlib/dlog"
)

type dlogEmitter struct {
	context.Context
}

func (l dlogEmitter) Emit(_ int, level log.Level, _ time.Time, format string, v ...interface{}) { //nolint:goprintffuncname // not our API
	switch level {
	case log.Debug:
		dlog.Debugf(l, format, v...)
	case log.Info:
		dlog.Infof(l, format, v...)
	case log.Warning:
		dlog.Warnf(l, format, v...)
	}
}

func InitLogger(ctx context.Context) {
	log.SetTarget(&dlogEmitter{Context: ctx})
	var gl log.Level
	switch dlog.MaxLogLevel(ctx) {
	case dlog.LogLevelInfo:
		gl = log.Info
	case dlog.LogLevelDebug, dlog.LogLevelTrace:
		gl = log.Debug
	default:
		gl = log.Warning
	}
	log.SetLevel(gl)
}
