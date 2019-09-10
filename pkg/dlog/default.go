package dlog

import (
	"sync"

	"github.com/sirupsen/logrus"
)

var (
	defaultLogger     Logger
	defaultLoggerOnce sync.Once
)

func getDefaultLogger() Logger {
	defaultLoggerOnce.Do(func() { defaultLogger = WrapLogrus(logrus.New()) })
	return defaultLogger
}
