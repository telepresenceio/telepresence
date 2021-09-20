package main

import (
	"context"

	"github.com/sirupsen/logrus"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

func makeBaseLogger(ctx context.Context, logLevel string) context.Context {
	logrusLogger := logrus.New()
	logrusFormatter := log.NewFormatter("2006-01-02 15:04:05.0000")
	logrusLogger.SetFormatter(logrusFormatter)

	log.SetLogrusLevel(logrusLogger, logLevel)

	logger := dlog.WrapLogrus(logrusLogger)
	dlog.SetFallbackLogger(logger)
	ctx = dlog.WithLogger(ctx, logger)
	return log.WithLevelSetter(ctx, logrusLogger)
}
