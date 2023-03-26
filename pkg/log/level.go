package log

import (
	"context"

	"github.com/sirupsen/logrus"
)

type setLogLevelContextKey struct{}

var DlogLevelNames = [5]string{ //nolint:gochecknoglobals // constant names
	"error",
	"warning",
	"info",
	"debug",
	"trace",
}

// SetLevel sets the log-level for the logger of the given context.
func SetLevel(ctx context.Context, logLevelStr string) {
	if setter, ok := ctx.Value(setLogLevelContextKey{}).(func(string)); ok {
		setter(logLevelStr)
	}
}

// WithLevelSetter enables setting the log-level of the given Logger by using the returned context as
// an argument to the SetLevel function.
func WithLevelSetter(ctx context.Context, logrusLogger *logrus.Logger) context.Context {
	return context.WithValue(ctx, setLogLevelContextKey{}, func(logLevelStr string) {
		SetLogrusLevel(logrusLogger, logLevelStr, true)
	})
}

// SetLogrusLevel sets the log-level of the given logger from logLevelStr and logs that to the logger.
func SetLogrusLevel(logrusLogger *logrus.Logger, logLevelStr string, logChange bool) {
	const defaultLogLevel = logrus.InfoLevel
	logLevel := defaultLogLevel
	var err error
	if logLevelStr != "" {
		if logLevel, err = logrus.ParseLevel(logLevelStr); err != nil {
			logLevel = defaultLogLevel
			logrusLogger.Errorf("%v, falling back to default %q", err, logLevel)
		}
	}

	if logrusLogger.Level != logLevel {
		logrusLogger.SetLevel(logLevel)
		logrusLogger.SetReportCaller(logLevel >= logrus.TraceLevel)
		if logChange {
			logrusLogger.Logf(logLevel, "Logging at this level %q", logLevel)
		}
	}
}
