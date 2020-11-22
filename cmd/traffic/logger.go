package main

import (
	"fmt"
	"os"

	"github.com/datawire/dlib/dlog"
	"github.com/sirupsen/logrus"
)

func makeBaseLogger() dlog.Logger {
	logrusLogger := logrus.New()
	logrusFormatter := &logrus.TextFormatter{
		TimestampFormat: "2006-01-02 15:04:05",
		FullTimestamp:   true,
	}
	logrusLogger.SetFormatter(logrusFormatter)
	logrusLogger.SetReportCaller(false)

	const defaultLogLevel = logrus.InfoLevel

	logLevelMessage := "Logging at this level"
	logLevelStr := os.Getenv("LOG_LEVEL")
	logLevel, err := logrus.ParseLevel(logLevelStr)

	switch {
	case logLevelStr == "": // not specified -> use default
		logLevel = defaultLogLevel
		logLevelMessage += " (default)"
	case err != nil: // Didn't parse -> use default and show error
		logLevel = defaultLogLevel
		logLevelMessage += fmt.Sprintf(" (LOG_LEVEL=%q -> %s)", logLevelStr, err.Error())
	default: // parsed successfully -> use that level
		logLevelMessage += fmt.Sprintf(" (LOG_LEVEL=%q)", logLevelStr)
	}

	logrusLogger.SetLevel(logLevel)
	logrusLogger.Log(logLevel, logLevelMessage)

	return dlog.WrapLogrus(logrusLogger)
}
