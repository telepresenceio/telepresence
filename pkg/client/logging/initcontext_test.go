package logging

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

type dtimeHook struct{}

func (dtimeHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (dtimeHook) Fire(entry *logrus.Entry) error {
	entry.Time = dtime.Now()
	return nil
}

func TestInitContext(t *testing.T) {
	const logName = "testing"

	ft := dtime.NewFakeTime()

	testSetup := func(t *testing.T) (ctx context.Context, logDir, logFile string) {
		t.Helper()
		ctx = dlog.NewTestContext(t, false)

		// Ensure that we use a temporary log dir
		logDir = t.TempDir()
		ctx = filelocation.WithAppUserLogDir(ctx, logDir)

		// Ensure that we never consider Stdout to be a terminal
		saveIsTerminal := IsTerminal
		IsTerminal = func(int) bool { return false }
		t.Cleanup(func() { IsTerminal = saveIsTerminal })

		// Use ft
		dtime.SetNow(ft.Now)
		t.Cleanup(func() { dtime.SetNow(time.Now) })

		// InitContext overrides both file descriptors 1/2 and the variables
		// os.Stdout/os.Stdin; so they need to be backed up and restored.
		saveStdout := os.Stdout
		saveStderr := os.Stderr
		var stdoutFd, stderrFd int
		if runtime.GOOS != "windows" {
			var err error
			stdoutFd, stderrFd, err = dupStd()
			require.NoError(t, err)
		}
		t.Cleanup(func() {
			os.Stdout = saveStdout
			os.Stderr = saveStderr
			if runtime.GOOS != "windows" {
				_ = restoreStd(stdoutFd, stderrFd)
			}
		})

		return ctx, logDir, filepath.Join(logDir, logName+".log")
	}

	closeLog := func(t *testing.T) {
		t.Helper()
		check := require.New(t)
		check.IsType(&RotatingFile{}, loggerForTest.Out)
		check.NoError(loggerForTest.Out.(*RotatingFile).Close())
	}

	t.Run("stdout and stderr", func(t *testing.T) {
		ctx, _, logFile := testSetup(t)
		check := require.New(t)

		c, err := InitContext(ctx, logName)
		loggerForTest.AddHook(&dtimeHook{})
		check.NoError(err)
		check.NotNil(c)
		defer closeLog(t)

		require.FileExists(t, logFile)

		infoMsg := "info"
		fmt.Fprintln(os.Stdout, infoMsg)
		time.Sleep(10 * time.Millisecond) // Ensure that message is logged before the next is produced

		ft.Step(time.Second)
		errMsg := "error"
		fmt.Fprintln(os.Stderr, errMsg)

		bs, err := ioutil.ReadFile(logFile)
		check.NoError(err)
		check.Contains(string(bs), fmt.Sprintf("%s\n%s\n", infoMsg, errMsg))
	})

	// This will fail on Windows
	if runtime.GOOS != "windows" {
		t.Run("captures output of builtin functions", func(t *testing.T) {
			ctx, _, logFile := testSetup(t)
			check := require.New(t)

			c, err := InitContext(ctx, logName)
			loggerForTest.AddHook(&dtimeHook{})
			check.NoError(err)
			check.NotNil(c)
			defer closeLog(t)

			msg := "some message"
			println(msg)
			check.FileExists(logFile)

			bs, err := ioutil.ReadFile(logFile)
			check.NoError(err)
			check.Equal(fmt.Sprintln(msg), string(bs))
		})
	}

	t.Run("next session rotates on write", func(t *testing.T) {
		ctx, logDir, logFile := testSetup(t)
		check := require.New(t)

		c, err := InitContext(ctx, logName)
		loggerForTest.AddHook(&dtimeHook{})
		check.NoError(err)
		check.NotNil(c)
		infoMsg := "info message"
		dlog.Info(c, infoMsg)
		closeLog(t)

		c, err = InitContext(ctx, logName)
		loggerForTest.AddHook(&dtimeHook{})
		check.NoError(err)
		check.NotNil(c)
		defer closeLog(t)

		check.FileExists(logFile)
		backupFile := filepath.Join(logDir, fmt.Sprintf("%s-%s.log", logName, dtime.Now().Format("20060102T150405")))

		// Nothing has been logged yet so no rotation has taken place.
		check.NoFileExists(backupFile)

		ft.Step(time.Second)
		infoTs := dtime.Now().Format("2006/01/02 15:04:05.0000")
		dlog.Info(c, infoMsg)
		backupFile = filepath.Join(logDir, fmt.Sprintf("%s-%s.log", logName, dtime.Now().Format("20060102T150405")))
		check.FileExists(backupFile)

		bs, err := ioutil.ReadFile(logFile)
		check.NoError(err)
		check.Equal(fmt.Sprintf("%s info     : %s\n", infoTs, infoMsg), string(bs))
	})

	t.Run("old files are removed", func(t *testing.T) {
		ctx, logDir, _ := testSetup(t)
		check := require.New(t)

		maxFiles := 5
		if me := os.Getenv("TELEPRESENCE_MAX_LOGFILES"); me != "" {
			if mx, err := strconv.Atoi(me); err == nil && mx >= 0 {
				maxFiles = mx
			}
		}
		for i := 0; i < maxFiles+2; i++ {
			ft.Step(24 * time.Hour)
			c, err := InitContext(ctx, logName)
			loggerForTest.AddHook(&dtimeHook{})
			check.NoError(err)
			check.NotNil(c)
			infoMsg := "info message"
			dlog.Info(c, infoMsg)
			closeLog(t)
		}
		// Give file remover some time to finish
		time.Sleep(100 * time.Millisecond)

		files, err := ioutil.ReadDir(logDir)
		check.NoError(err)
		check.Equal(maxFiles, len(files))
	})
}
