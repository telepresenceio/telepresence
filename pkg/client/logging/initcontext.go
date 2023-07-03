package logging

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	tlog "github.com/telepresenceio/telepresence/v2/pkg/log"
)

// loggerForTest exposes internals to initcontext_test.go.
var loggerForTest *logrus.Logger //nolint:gochecknoglobals // used by unit tests only

// InitContext sets up standard Telepresence logging for a background process.
func InitContext(ctx context.Context, name string, strategy RotationStrategy, captureStd bool) (context.Context, error) {
	logger := logrus.StandardLogger()
	loggerForTest = logger

	// Start with InfoLevel so that the config is read using that level
	logger.SetLevel(logrus.InfoLevel)
	logger.ReportCaller = false // turned on when level >= logrus.TraceLevel

	if captureStd && IsTerminal(int(os.Stdout.Fd())) {
		logger.Formatter = tlog.NewFormatter("15:04:05.0000")
	} else {
		logger.Formatter = tlog.NewFormatter("2006-01-02 15:04:05.0000")
		maxFiles := uint16(5)

		// TODO: Also make this a configurable setting in config.yml
		if me := os.Getenv("TELEPRESENCE_MAX_LOGFILES"); me != "" {
			if mx, err := strconv.Atoi(me); err == nil && mx >= 0 {
				maxFiles = uint16(mx)
			}
		}
		rf, err := OpenRotatingFile(ctx, filepath.Join(filelocation.AppUserLogDir(ctx), name+".log"), "20060102T150405", true, 0o600, strategy, maxFiles)
		if err != nil {
			return ctx, err
		}
		logger.SetOutput(rf)

		if captureStd {
			if err := dupToStdOut(rf.file.(*os.File)); err != nil {
				return ctx, err
			}
			if err := dupToStdErr(rf.file.(*os.File)); err != nil {
				return ctx, err
			}
		}

		// Configure the standard logger to write without any fields and with prefix "stdlog"
		log.SetOutput(logger.Writer())
		log.SetPrefix("stdlog : ")
		log.SetFlags(0)
	}

	ctx = dlog.WithLogger(ctx, dlog.WrapLogrus(logger))

	// Read the config and set the configured level.
	logLevels := client.GetConfig(ctx).LogLevels()
	level := logLevels.UserDaemon
	if name == "daemon" {
		level = logLevels.RootDaemon
	}
	tlog.SetLogrusLevel(logger, level.String(), false)
	ctx = tlog.WithLevelSetter(ctx, logger)
	return ctx, nil
}

func SummarizeLog(ctx context.Context, name string) (string, error) {
	filename := filepath.Join(filelocation.AppUserLogDir(ctx), name+".log")
	file, err := dos.Open(ctx, filename)
	if err != nil {
		if os.IsNotExist(err) {
			err = nil
		}
		return "", err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)

	errorCount := 0
	for scanner.Scan() {
		// XXX: is there a better way to detect error lines?
		txt := scanner.Text()
		parts := strings.Fields(txt)
		if len(parts) < 3 {
			continue
		}
		switch parts[2] {
		case "error":
			errorCount++
		case "info":
			if strings.Contains(txt, "-- Starting new session") {
				// Start over. No use counting errors from previous sessions
				errorCount = 0
			}
		}
	}
	if errorCount == 0 {
		return "", nil
	}
	desc := fmt.Sprintf("%d error", errorCount)
	if errorCount > 1 {
		desc += "s"
	}

	return fmt.Sprintf("See logs for details (%s found): %q", desc, filename), nil
}
