package dlog

import (
	"io"
	"log"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type logrusLogger interface {
	WithField(key string, value interface{}) *logrus.Entry
	WriterLevel(level logrus.Level) *io.PipeWriter

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

type logrusWrapper struct {
	logrusLogger
}

func (l logrusWrapper) WithField(key string, value interface{}) Logger {
	return logrusWrapper{l.logrusLogger.WithField(key, value)}
}

func (l logrusWrapper) StdLogger(level LogLevel) *log.Logger {
	logrusLevel, ok := map[LogLevel]logrus.Level{
		LogLevelError: logrus.ErrorLevel,
		LogLevelWarn:  logrus.WarnLevel,
		LogLevelInfo:  logrus.InfoLevel,
		LogLevelDebug: logrus.DebugLevel,
		LogLevelTrace: logrus.TraceLevel,
	}[level]
	if !ok {
		panic(errors.Errorf("invalid LogLevel: %d", level))
	}
	return log.New(l.logrusLogger.WriterLevel(logrusLevel), "", 0)
}

// WrapLogrus converts a logrus *Logger (or *Entry) into a generic
// Logger.
//
// You should only really ever call WrapLogrus from the initial
// process set up (i.e. directly inside your 'main()' function), and
// you should pass the result directly to WithLogger.
func WrapLogrus(in logrusLogger) Logger {
	return logrusWrapper{in}
}
