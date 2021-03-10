package logging

import (
	"context"
	"os"

	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh/terminal"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

// IsTerminal returns whether the given file descriptor is a terminal
var IsTerminal = terminal.IsTerminal

// InitContext sets up standard Telepresence logging for a background process
func InitContext(ctx context.Context, name string) (context.Context, error) {
	logger := logrus.StandardLogger()
	logger.SetLevel(logrus.DebugLevel)

	if IsTerminal(int(os.Stdout.Fd())) {
		logger.Formatter = NewFormatter("15:04:05")
	} else {
		logger.Formatter = NewFormatter("2006/01/02 15:04:05")
		dir, err := filelocation.AppUserLogDir(ctx)
		if err != nil {
			return ctx, err
		}
		rf, err := OpenRotatingFile(dir, name+".log", "20060102T150405", true, true, 0600, NewRotateOnce(), 5)
		if err != nil {
			return ctx, err
		}
		logger.SetOutput(rf)
	}
	return dlog.WithLogger(ctx, dlog.WrapLogrus(logger)), nil
}
