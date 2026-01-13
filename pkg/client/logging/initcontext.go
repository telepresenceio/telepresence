package logging

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	stdLog "log"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/clog/handler"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

// rotatingFileForTest exposes internals to initcontext_test.go.
var rotatingFileForTest *RotatingFile //nolint:gochecknoglobals // used by unit tests only

type splitErrorWriter struct {
	outWriter io.Writer
	errWriter io.Writer
}

func (w *splitErrorWriter) Write(level slog.Level, p []byte) (n int, err error) {
	if level >= slog.LevelError {
		return w.errWriter.Write(p)
	}
	return w.outWriter.Write(p)
}

// InitContext sets up standard Telepresence logging for a background process.
func InitContext(ctx context.Context, logFile string, logLevel slog.Level, strategy RotationStrategy, captureStd bool) (context.Context, error) {
	ctx = clog.WithTreeLevel(ctx, logLevel)

	var opts []handler.Option
	initStdLog := false
	switch logFile {
	case "stdout":
		opts = append(opts, handler.TimeFormat("15:04:05.0000"), handler.Output(os.Stdout))
	case "", "-", "stderr":
		opts = append(opts, handler.TimeFormat("15:04:05.0000"), handler.Output(os.Stderr))
	case "managed":
		// "managed" is a special case used by the daemon to log to stdout and stderr. It's
		// assumed that the caller will add a timestamp, and that level is implicit for errors.
		opts = append(opts, handler.TimeFormat(""), handler.HideLevel(slog.LevelError), handler.LevelOutput(&splitErrorWriter{os.Stdout, os.Stderr}))
	case "std":
		// "std" is a special case used by the daemon to log to stdout and stderr. Contrary to
		// "managed", it's not assumed that the caller will add a timestamp or that the level is implicit.
		opts = append(opts, handler.TimeFormat("2006-01-02 15:04:05.0000"), handler.LevelOutput(&splitErrorWriter{os.Stdout, os.Stderr}))
	default:
		initStdLog = true
		maxFiles := uint16(5)

		// TODO: Also make this a configurable setting in config.yml
		if me := os.Getenv("TELEPRESENCE_MAX_LOGFILES"); me != "" {
			if mx, err := strconv.Atoi(me); err == nil && mx >= 0 {
				maxFiles = uint16(mx)
			}
		}

		// Validate the path before using it.
		logFile, err := ValidateLogFilePath(logFile)
		if err != nil {
			return ctx, err
		}
		rf, err := OpenRotatingFile(ctx, logFile, "20060102T150405", true, 0o600, strategy, maxFiles)
		if err != nil {
			return ctx, err
		}
		rotatingFileForTest = rf
		if captureStd {
			rfFile := rf.file.(*os.File)
			err = dupStdOut(rfFile)
			if err != nil {
				_ = rf.Close()
				return ctx, err
			}
			err = dupStdErr(rfFile)
			if err != nil {
				_ = rf.Close()
				return ctx, err
			}
		}
		opts = append(opts, handler.TimeFormat("2006-01-02 15:04:05.0000"), handler.Output(rf))
	}

	sl := slog.New(handler.NewText(append(opts, handler.LevelEnabler(clog.TreeEnabled))...))
	slog.SetDefault(sl)
	if initStdLog {
		stl := clog.StdLogger(ctx, logLevel)
		stdLog.SetOutput(stl.Writer())
		stdLog.SetFlags(stl.Flags())
		stdLog.SetPrefix("stdlog : ")
	}
	return clog.WithLogger(ctx, sl), nil
}

// ValidateLogFilePath ensures that the log file path is valid and that the parent directory exists or can be created.
func ValidateLogFilePath(logFile string) (string, error) {
	// Convert to an absolute path. Abs ensures Clean.
	logFile, err := filepath.Abs(logFile)
	if err != nil {
		return "", errcat.User.Errorf(err, "invalid log file path")
	}

	// Verify that the parent directory exists or can be created
	dir := filepath.Dir(logFile)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", errcat.User.Errorf(err, "cannot create log directory %q", dir)
	}
	return logFile, nil
}

func SummarizeLog(ctx context.Context, name string) (string, error) {
	filename := filepath.Join(filelocation.AppUserLogDir(ctx), name+".log")
	file, err := dos.Open(ctx, filename)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
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
