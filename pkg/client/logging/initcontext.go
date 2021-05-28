package logging

import (
	"context"
	"os"
	"path/filepath"

	"github.com/telepresenceio/telepresence/v2/pkg/client"

	"github.com/sirupsen/logrus"
	"golang.org/x/term"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

// IsTerminal returns whether the given file descriptor is a terminal
var IsTerminal = term.IsTerminal

// loggerForTest exposes internals to initcontext_test.go
var loggerForTest *logrus.Logger

// InitContext sets up standard Telepresence logging for a background process
func InitContext(ctx context.Context, name string) (context.Context, error) {
	logger := logrus.New()
	loggerForTest = logger
	logLevels := client.GetConfig(ctx).LogLevels
	if name == "daemon" {
		logger.SetLevel(logLevels.RootDaemon)
	} else if name == "connector" {
		logger.SetLevel(logLevels.UserDaemon)
	}
	logger.ReportCaller = true

	if IsTerminal(int(os.Stdout.Fd())) {
		logger.Formatter = NewFormatter("15:04:05.0000")
	} else {
		logger.Formatter = NewFormatter("2006/01/02 15:04:05.0000")
		dir, err := filelocation.AppUserLogDir(ctx)
		if err != nil {
			return ctx, err
		}
		rf, err := OpenRotatingFile(filepath.Join(dir, name+".log"), "20060102T150405", true, true, 0600, NewRotateOnce(), 5)
		if err != nil {
			return ctx, err
		}
		logger.SetOutput(rf)
	}
	return dlog.WithLogger(ctx, dlog.WrapLogrus(logger)), nil
}
