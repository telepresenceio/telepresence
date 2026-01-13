package logging

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func TestInitContext(t *testing.T) {
	const logName = "testing"

	testSetup := func(t *testing.T) (ctx context.Context, logDir, logFile string) {
		t.Helper()
		ctx = testutil.NewContext(t, false)
		ctx, cancel := context.WithCancel(ctx)
		env, err := client.LoadEnv()
		if err != nil {
			t.Fatal(err)
		}
		ctx = client.WithEnv(ctx, &env)

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
			cancel()
		})

		return ctx, logDir, filepath.Join(logDir, logName+".log")
	}

	t.Run("stdout and stderr", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, _, logFile := testSetup(t)
			check := require.New(t)

			clog.Info(ctx, "test setup")

			c, err := InitContext(ctx, logFile, slog.LevelInfo, NewRotateOnce(), true)
			check.NoError(err)
			check.NotNil(c)

			require.FileExists(t, logFile)

			infoMsg := "info"
			fmt.Fprintln(os.Stdout, infoMsg)
			time.Sleep(10 * time.Millisecond) // Ensure that message is logged before the next is produced

			errMsg := "error"
			fmt.Fprintln(os.Stderr, errMsg)
			time.Sleep(30 * time.Millisecond)

			bs, err := os.ReadFile(logFile)
			check.NoError(err)
			s := string(bs)
			check.Contains(s, infoMsg)
			check.Contains(s, errMsg)
		})
	})

	t.Run("captures output of builtin functions", func(t *testing.T) {
		ctx, _, logFile := testSetup(t)
		check := require.New(t)

		c, err := InitContext(ctx, logFile, slog.LevelInfo, NewRotateOnce(), true)
		check.NoError(err)
		check.NotNil(c)

		msg := "some message"
		println(msg) //nolint:forbidigo // we're testing this builtin function
		check.FileExists(logFile)
		time.Sleep(30 * time.Millisecond)
		bs, err := os.ReadFile(logFile)
		check.NoError(err)
		check.Contains(string(bs), msg)
	})

	t.Run("captures output of standard logger", func(t *testing.T) {
		ctx, _, logFile := testSetup(t)
		check := require.New(t)

		c, err := InitContext(ctx, logFile, slog.LevelInfo, NewRotateOnce(), true)
		check.NoError(err)
		check.NotNil(c)

		msg := "some message"
		log.Print(msg)
		time.Sleep(100 * time.Millisecond)
		check.FileExists(logFile)

		bs, err := os.ReadFile(logFile)
		check.NoError(err)
		check.Contains(string(bs), fmt.Sprintf("INFO  stdlog : %s\n", msg))
	})

	t.Run("next session rotates on write", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, logDir, logFile := testSetup(t)
			check := require.New(t)

			c, err := InitContext(ctx, logFile, slog.LevelInfo, NewRotateOnce(), false)
			check.NoError(err)
			check.NotNil(c)
			infoMsg := "info message"
			clog.Info(c, infoMsg)
			time.Sleep(time.Second)

			c, err = InitContext(ctx, logFile, slog.LevelInfo, NewRotateOnce(), false)
			check.NoError(err)
			check.NotNil(c)
			clog.Info(c, infoMsg)
			check.FileExists(logFile)

			infoTs := time.Now().Format("2006-01-02 15:04:05.0000")
			backupFile := filepath.Join(logDir, fmt.Sprintf("%s-%s.log", logName, time.Now().Format("20060102T150405")))
			check.FileExists(backupFile)

			bs, err := os.ReadFile(logFile)
			check.NoError(err)
			check.Contains(string(bs), fmt.Sprintf("%s INFO  %s\n", infoTs, infoMsg))
		})
	})

	t.Run("birthtime updates after rotate", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, _, logFile := testSetup(t)
			check := require.New(t)

			c, err := InitContext(ctx, logFile, slog.LevelInfo, NewRotateOnce(), false)
			check.NoError(err)
			check.NotNil(c)
			clog.Info(c, "info message")
			check.NotNil(rotatingFileForTest)
			bt1 := rotatingFileForTest.birthTime

			c, err = InitContext(ctx, logFile, slog.LevelInfo, NewRotateOnce(), false)
			check.NoError(err)
			check.NotNil(c)
			clog.Info(c, "info message")
			check.NotNil(rotatingFileForTest)
			bt2 := rotatingFileForTest.birthTime
			check.Equal(bt1, bt2)
		})
	})

	t.Run("next session appends when no rotate", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, _, logFile := testSetup(t)
			check := require.New(t)

			c, err := InitContext(ctx, logFile, slog.LevelInfo, RotateNever, false)
			check.NoError(err)
			check.NotNil(c)
			infoMsg1 := "info message 1"
			clog.Info(c, infoMsg1)

			c, err = InitContext(ctx, logFile, slog.LevelInfo, RotateNever, false)
			check.NoError(err)
			check.NotNil(c)
			infoMsg2 := "info message 2"
			clog.Info(c, infoMsg2)

			bs, err := os.ReadFile(logFile)
			check.NoError(err)
			infoTs := time.Now().Format("2006-01-02 15:04:05.0000")
			check.Contains(string(bs), fmt.Sprintf("%s INFO  %s\n", infoTs, infoMsg1))
			check.Contains(string(bs), fmt.Sprintf("%s INFO  %s\n", infoTs, infoMsg2))
		})
	})

	t.Run("old files are removed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, logDir, logFile := testSetup(t)
			check := require.New(t)

			maxFiles := 5
			if me := os.Getenv("TELEPRESENCE_MAX_LOGFILES"); me != "" {
				if mx, err := strconv.Atoi(me); err == nil && mx >= 0 {
					maxFiles = mx
				}
			}
			for i := 0; i < maxFiles+2; i++ {
				time.Sleep(24 * time.Hour)
				c, err := InitContext(ctx, logFile, slog.LevelInfo, NewRotateOnce(), false)
				check.NoError(err)
				check.NotNil(c)
				infoMsg := "info message"
				clog.Info(c, infoMsg)
			}
			// Give file remover some time to finish
			time.Sleep(100 * time.Millisecond)

			files, err := os.ReadDir(logDir)
			check.NoError(err)
			check.Equal(maxFiles, len(files))
		})
	})
}
