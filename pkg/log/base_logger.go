package log

import (
	"context"

	"github.com/sirupsen/logrus"

	"github.com/datawire/dlib/dlog"
)

func MakeBaseLogger(ctx context.Context, logLevel string) context.Context {
	logrusLogger := logrus.StandardLogger()
	logrusFormatter := NewFormatter("2006-01-02 15:04:05.0000")
	logrusLogger.SetFormatter(logrusFormatter)

	SetLogrusLevel(logrusLogger, logLevel, false)

	logger := dlog.WrapLogrus(logrusLogger)
	dlog.SetFallbackLogger(logger)
	ctx = dlog.WithLogger(ctx, logger)
	return WithLevelSetter(ctx, logrusLogger)
}
