package log

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"
)

type setLogLevelContextKey struct{}

// SetLevel sets the log-level for the logger of the given context
func SetLevel(ctx context.Context, logLevelStr string) {
	if setter, ok := ctx.Value(setLogLevelContextKey{}).(func(string)); ok {
		setter(logLevelStr)
	}
}

// WithLevelSetter enables setting the log-level of the given Logger by using the returned context as
// an argument to the SetLevel function
func WithLevelSetter(ctx context.Context, logrusLogger *logrus.Logger) context.Context {
	return context.WithValue(ctx, setLogLevelContextKey{}, func(logLevelStr string) {
		SetLogrusLevel(logrusLogger, logLevelStr)
	})
}

// SetLogrusLevel sets the log-level of the given logger from logLevelStr and logs that to the logger.
func SetLogrusLevel(logrusLogger *logrus.Logger, logLevelStr string) {
	const defaultLogLevel = logrus.InfoLevel
	logLevelMessage := "Logging at this level"
	logLevel, err := logrus.ParseLevel(logLevelStr)

	switch {
	case logLevelStr == "": // not specified -> use default
		logLevel = defaultLogLevel
		logLevelMessage += " (default)"
	case err != nil: // Didn't parse -> use default and show error
		logLevel = defaultLogLevel
		logLevelMessage += fmt.Sprintf(" (LOG_LEVEL=%q -> %v)", logLevelStr, err)
	default: // parsed successfully -> use that level
		logLevelMessage += fmt.Sprintf(" (LOG_LEVEL=%q)", logLevelStr)
	}
	logrusLogger.SetLevel(logLevel)
	logrusLogger.SetReportCaller(logLevel >= logrus.TraceLevel)
	logrusLogger.Log(logLevel, logLevelMessage)
}
