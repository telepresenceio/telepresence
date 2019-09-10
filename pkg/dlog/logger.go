// Package dlog implements a generic logger facade.
//
// There are two first-class things of value in this package:
//
// First: The Logger interface.  This is a simple structured logging
// interface that is mostly trivial to implement on top of most
// logging backends, and allows library code to not need to care about
// what specific logging system the calling program uses.
//
// Second: The WithLogger, GetLogger, and WithLoggerField functions
// for tracking logger context.  These allow you to painlessly
// associate a logger with a context.
//
// If you are writing library code and want a logger, then you should
// take a context.Context as an argument, and then call GetLogger on
// that Context argument.
package dlog

import (
	"context"
	"log"
)

// Logger is a generic logging interface that most loggers implement,
// so that consumers don't need to care about the actual log
// implementation.
//
// Note that unlike logrus.FieldLogger, it does not include Fatal or
// Panic logging options.  Do proper error handling!  Return those
// errors!
type Logger interface {
	WithField(key string, value interface{}) Logger
	StdLogger(LogLevel) *log.Logger

	Tracef(format string, args ...interface{})
	Debugf(format string, args ...interface{})
	Infof(format string, args ...interface{})
	Printf(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Warningf(format string, args ...interface{})
	Errorf(format string, args ...interface{})

	Trace(args ...interface{})
	Debug(args ...interface{})
	Info(args ...interface{})
	Print(args ...interface{})
	Warn(args ...interface{})
	Warning(args ...interface{})
	Error(args ...interface{})

	Traceln(args ...interface{})
	Debugln(args ...interface{})
	Infoln(args ...interface{})
	Println(args ...interface{})
	Warnln(args ...interface{})
	Warningln(args ...interface{})
	Errorln(args ...interface{})
}

// LogLevel is an abstracted common log-level type for for
// Logger.StdLogger().
type LogLevel uint32

const (
	// LogLevelError is for errors that should definitely be noted.
	LogLevelError LogLevel = iota
	// LogLevelWarn is for non-critical entries that deserve eyes.
	LogLevelWarn
	// LogLevelInfo is for general operational entries about what's
	// going on inside the application.
	LogLevelInfo
	// LogLevelDebug is for debugging.  Very verbose logging.
	LogLevelDebug
	// LogLevelTrace is for extreme debugging.  Even finer-grained
	// informational events than the Debug.
	LogLevelTrace
)

// WithLogger returns a copy of ctx with logger associated with it,
// for future calls to GetLogger.
//
// You should only really ever call WithLogger from the initial
// process set up (i.e. directly inside your 'main()' function).
func WithLogger(ctx context.Context, logger Logger) context.Context {
	return context.WithValue(ctx, loggerContextKey{}, logger)
}

// WithLoggerField is a convenience wrapper for
//
//     WithLogger(ctx, GetLogger(ctx).WithField(key, value))
func WithLoggerField(ctx context.Context, key string, value interface{}) context.Context {
	return WithLogger(ctx, GetLogger(ctx).WithField(key, value))
}

// GetLogger returns the Logger associated with ctx.  If ctx has no
// Logger associated with it, a "default" logger is returned.  This
// function always returns a usable logger.
func GetLogger(ctx context.Context) Logger {
	logger := ctx.Value(loggerContextKey{})
	if logger == nil {
		return getDefaultLogger()
	}
	return logger.(Logger)
}

type loggerContextKey struct{}
