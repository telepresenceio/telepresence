package logging

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
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
		env, err := client.LoadEnv()
		if err != nil {
			t.Fatal(err)
		}
		ctx = client.WithEnv(ctx, env)

		// Ensure that we use a temporary log dir
		logDir = t.TempDir()
		ctx = filelocation.WithAppUserLogDir(ctx, logDir)

		cfg, err := client.LoadConfig(ctx)
		require.NoError(t, err)
		ctx = client.WithConfig(ctx, cfg)

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
		restoreStd, err := dupStd()
		require.NoError(t, err)
		t.Cleanup(func() {
			os.Stdout = saveStdout
			os.Stderr = saveStderr
			restoreStd()
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

		c, err := InitContext(ctx, logName, NewRotateOnce(), true)
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
		time.Sleep(30 * time.Millisecond)

		bs, err := os.ReadFile(logFile)
		check.NoError(err)
		s := string(bs)
		check.Contains(s, infoMsg)
		check.Contains(s, errMsg)
	})

	t.Run("captures output of builtin functions", func(t *testing.T) {
		ctx, _, logFile := testSetup(t)
		check := require.New(t)

		c, err := InitContext(ctx, logName, NewRotateOnce(), true)
		loggerForTest.AddHook(&dtimeHook{})
		check.NoError(err)
		check.NotNil(c)
		defer closeLog(t)

		msg := "some message"
		println(msg)
		check.FileExists(logFile)
		time.Sleep(30 * time.Millisecond)
		bs, err := os.ReadFile(logFile)
		check.NoError(err)
		check.Contains(string(bs), msg)
	})

	t.Run("captures output of standard logger", func(t *testing.T) {
		ctx, _, logFile := testSetup(t)
		check := require.New(t)

		c, err := InitContext(ctx, logName, NewRotateOnce(), true)
		loggerForTest.AddHook(&dtimeHook{})
		check.NoError(err)
		check.NotNil(c)
		defer closeLog(t)

		msg := "some message"
		log.Print(msg)
		time.Sleep(100 * time.Millisecond)
		check.FileExists(logFile)

		bs, err := os.ReadFile(logFile)
		check.NoError(err)
		check.Contains(string(bs), fmt.Sprintf("info    stdlog : %s\n", msg))
	})

	t.Run("next session rotates on write", func(t *testing.T) {
		ctx, logDir, logFile := testSetup(t)
		check := require.New(t)

		c, err := InitContext(ctx, logName, NewRotateOnce(), true)
		loggerForTest.AddHook(&dtimeHook{})
		check.NoError(err)
		check.NotNil(c)
		infoMsg := "info message"
		dlog.Info(c, infoMsg)
		closeLog(t)
		ft.Step(time.Second)

		c, err = InitContext(ctx, logName, NewRotateOnce(), true)
		loggerForTest.AddHook(&dtimeHook{})
		check.NoError(err)
		check.NotNil(c)
		dlog.Info(c, infoMsg)
		check.FileExists(logFile)
		defer closeLog(t)

		infoTs := dtime.Now().Format("2006-01-02 15:04:05.0000")
		backupFile := filepath.Join(logDir, fmt.Sprintf("%s-%s.log", logName, dtime.Now().Format("20060102T150405")))
		check.FileExists(backupFile)

		bs, err := os.ReadFile(logFile)
		check.NoError(err)
		check.Contains(string(bs), fmt.Sprintf("%s info    %s\n", infoTs, infoMsg))
	})

	t.Run("birthtime updates after rotate", func(t *testing.T) {
		ctx, _, _ := testSetup(t)
		check := require.New(t)

		c, err := InitContext(ctx, logName, NewRotateOnce(), true)
		loggerForTest.AddHook(&dtimeHook{})
		check.NoError(err)
		check.NotNil(c)
		dlog.Info(c, "info message")
		bt1 := loggerForTest.Out.(*RotatingFile).birthTime
		closeLog(t)

		c, err = InitContext(ctx, logName, NewRotateOnce(), true)
		loggerForTest.AddHook(&dtimeHook{})
		check.NoError(err)
		check.NotNil(c)
		dlog.Info(c, "info message")
		bt2 := loggerForTest.Out.(*RotatingFile).birthTime
		closeLog(t)
		check.Equal(bt1, bt2)
	})

	t.Run("next session appends when no rotate", func(t *testing.T) {
		ctx, _, logFile := testSetup(t)
		check := require.New(t)

		c, err := InitContext(ctx, logName, RotateNever, true)
		loggerForTest.AddHook(&dtimeHook{})
		check.NoError(err)
		check.NotNil(c)
		infoMsg1 := "info message 1"
		dlog.Info(c, infoMsg1)
		closeLog(t)

		c, err = InitContext(ctx, logName, RotateNever, true)
		loggerForTest.AddHook(&dtimeHook{})
		check.NoError(err)
		check.NotNil(c)
		infoMsg2 := "info message 2"
		dlog.Info(c, infoMsg2)
		closeLog(t)

		bs, err := os.ReadFile(logFile)
		check.NoError(err)
		infoTs := dtime.Now().Format("2006-01-02 15:04:05.0000")
		check.Contains(string(bs), fmt.Sprintf("%s info    %s\n", infoTs, infoMsg1))
		check.Contains(string(bs), fmt.Sprintf("%s info    %s\n", infoTs, infoMsg2))
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
			c, err := InitContext(ctx, logName, NewRotateOnce(), true)
			loggerForTest.AddHook(&dtimeHook{})
			check.NoError(err)
			check.NotNil(c)
			infoMsg := "info message"
			dlog.Info(c, infoMsg)
			closeLog(t)
		}
		// Give file remover some time to finish
		time.Sleep(100 * time.Millisecond)

		files, err := os.ReadDir(logDir)
		check.NoError(err)
		check.Equal(maxFiles, len(files))
	})
}
