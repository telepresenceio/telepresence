package logging

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

// loggerForTest exposes internals to initcontext_test.go
var loggerForTest *logrus.Logger

// InitContext sets up standard Telepresence logging for a background process
func InitContext(ctx context.Context, name string, strategy RotationStrategy) (context.Context, error) {
	logger := logrus.New()
	loggerForTest = logger

	// Start with DebugLevel so that the config is read using that level
	logger.SetLevel(logrus.DebugLevel)
	logger.ReportCaller = false // turned on when level >= logrus.TraceLevel

	if IsTerminal(int(os.Stdout.Fd())) {
		logger.Formatter = log.NewFormatter("15:04:05.0000")
	} else {
		logger.Formatter = log.NewFormatter("2006-01-02 15:04:05.0000")
		dir, err := filelocation.AppUserLogDir(ctx)
		if err != nil {
			return ctx, err
		}
		maxFiles := uint16(5)

		// TODO: Also make this a configurable setting in config.yml
		if me := os.Getenv("TELEPRESENCE_MAX_LOGFILES"); me != "" {
			if mx, err := strconv.Atoi(me); err == nil && mx >= 0 {
				maxFiles = uint16(mx)
			}
		}
		rf, err := OpenRotatingFile(filepath.Join(dir, name+".log"), "20060102T150405", true, true, 0600, strategy, maxFiles)
		if err != nil {
			return ctx, err
		}
		logger.SetOutput(rf)
	}
	ctx = dlog.WithLogger(ctx, dlog.WrapLogrus(logger))

	// Read the config and set the configured level.
	logLevels := client.GetConfig(ctx).LogLevels
	level := logrus.InfoLevel
	if name == "daemon" {
		level = logLevels.RootDaemon
	} else if name == "connector" {
		level = logLevels.UserDaemon
	}
	log.SetLogrusLevel(logger, level.String())
	ctx = log.WithLevelSetter(ctx, logger)
	return ctx, nil
}

func SummarizeLog(ctx context.Context, name string) (string, error) {
	dir, err := filelocation.AppUserLogDir(ctx)
	if err != nil {
		return "", err
	}

	filename := filepath.Join(dir, name+".log")
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)

	errorCount := 0
	for scanner.Scan() {
		// XXX: is there a better way to detect error lines?
		parts := strings.Fields(scanner.Text())
		if len(parts) > 2 && parts[2] == "error" {
			errorCount++
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
