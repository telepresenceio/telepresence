package logging

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
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
	// Ensure that we use a temporary logging.Dir and never consider Stdout to be a terminal
	saveDir := Dir
	saveIsTerminal := IsTerminal
	logDir, err := ioutil.TempDir("", "rotating-log-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		Dir = saveDir
		IsTerminal = saveIsTerminal
		_ = os.RemoveAll(logDir)
	}()

	Dir = func() string {
		return logDir
	}

	IsTerminal = func(int) bool {
		return false
	}

	ft := dtime.NewFakeTime()
	dtime.SetNow(ft.Now)

	// Ensure that logger timestamps using dtime.Now()
	lrLogger := logrus.StandardLogger()
	lrLogger.AddHook(&dtimeHook{})

	nowFormatted := func() string {
		return dtime.Now().Format("2006/01/02 15:04:05")
	}

	// The file descriptors 1 and 2 and os.Stdout, and os.Stdin needs to be backed up so that
	// they can be restored after each test
	saveStdout := os.Stdout
	saveStderr := os.Stderr

	// The duplicates are needed so that the 1 and 2 descriptors can be restored after each test.
	stdoutFd, err := unix.Dup(1)
	require.NoError(t, err)

	stderrFd, err := unix.Dup(2)
	require.NoError(t, err)

	afterEach := func(t *testing.T) {
		os.Stdout = saveStdout
		os.Stderr = saveStderr
		_ = unix.Dup2(stdoutFd, 1)
		_ = unix.Dup2(stderrFd, 2)

		t.Helper()
		check := require.New(t)
		files, err := ioutil.ReadDir(logDir)
		check.NoError(err)
		for _, file := range files {
			check.NoError(os.Remove(filepath.Join(logDir, file.Name())))
		}
	}

	const logName = "testing"
	closeLog := func(t *testing.T) {
		t.Helper()
		check := require.New(t)
		check.IsType(&RotatingFile{}, lrLogger.Out)
		check.NoError(lrLogger.Out.(*RotatingFile).Close())
	}

	logFile := filepath.Join(logDir, logName+".log")

	t.Run("stdout and stderr", func(t *testing.T) {
		defer afterEach(t)
		check := require.New(t)

		c, err := InitContext(context.Background(), logName)
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
		check.Equal(fmt.Sprintf("%s\n%s\n", infoMsg, errMsg), string(bs))
	})

	t.Run("captures output of builtin functions", func(t *testing.T) {
		defer afterEach(t)
		check := require.New(t)

		c, err := InitContext(context.Background(), logName)
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

	t.Run("next session rotates on write", func(t *testing.T) {
		defer afterEach(t)
		check := require.New(t)

		c, err := InitContext(context.Background(), logName)
		check.NoError(err)
		check.NotNil(c)
		infoMsg := "info message"
		dlog.Info(c, infoMsg)
		closeLog(t)

		c, err = InitContext(context.Background(), logName)
		check.NoError(err)
		check.NotNil(c)
		defer closeLog(t)

		check.FileExists(logFile)
		backupFile := filepath.Join(logDir, fmt.Sprintf("%s-%s.log", logName, dtime.Now().Format("20060102T150405")))

		// Nothing has been logged yet so no rotation has taken place.
		check.NoFileExists(backupFile)

		ft.Step(time.Second)
		infoTs := nowFormatted()
		dlog.Info(c, infoMsg)
		backupFile = filepath.Join(logDir, fmt.Sprintf("%s-%s.log", logName, dtime.Now().Format("20060102T150405")))
		check.FileExists(backupFile)

		bs, err := ioutil.ReadFile(logFile)
		check.NoError(err)
		check.Equal(fmt.Sprintf("%s %s\n", infoTs, infoMsg), string(bs))
	})

	t.Run("old files are removed", func(t *testing.T) {
		defer afterEach(t)
		check := require.New(t)

		for i := 0; i < 7; i++ {
			ft.Step(24 * time.Hour)
			c, err := InitContext(context.Background(), logName)
			check.NoError(err)
			check.NotNil(c)
			infoMsg := "info message"
			dlog.Info(c, infoMsg)
			closeLog(t)
		}
		files, err := ioutil.ReadDir(logDir)
		check.NoError(err)
		check.Equal(5, len(files))
	})
}
