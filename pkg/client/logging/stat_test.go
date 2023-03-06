package logging_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
)

var osHasBTime = true

func TestFStat(t *testing.T) {
	btimeIsCTime := testFStat(t, runtime.GOOS == "linux")
	if btimeIsCTime && osHasBTime {
		// The kernel supports btime, but the filesystem doesn't.  Set TMPDIR to be
		// $HOME/tmp, on the assumption that $HOME is on a "big boy" filesystem and thus
		// supports btime.
		t.Run("tmpdirInHome", func(t *testing.T) {
			os.Setenv("TMPDIR", filepath.Join(os.Getenv("HOME"), "tmp"))
			err := os.Mkdir(os.Getenv("TMPDIR"), 0o777)
			if err != nil && !errors.Is(err, os.ErrExist) {
				t.Fatal(err)
			}
			testFStat(t, false)
		})
	}
}

func testFStat(t *testing.T, okIfBTimeIsCTime bool) (btimeIsCTime bool) {
	const (
		fsVsClockLeeway = 1 * time.Second // many filesystems only have second precision
		minDelta        = 2 * time.Second
	)

	ctx := dlog.NewTestContext(t, false)
	ctx = client.WithEnv(ctx, &client.Env{})
	filename := filepath.Join(t.TempDir(), "stamp.txt")
	withFile := func(flags int, fn func(dos.File)) (time.Time, time.Time) {
		before := time.Now()
		time.Sleep(fsVsClockLeeway)
		file, err := dos.OpenFile(ctx, filename, flags, 0o666)
		require.NoError(t, err)
		require.NotNil(t, file)
		fn(file)
		require.NoError(t, file.Close())
		time.Sleep(fsVsClockLeeway)
		after := time.Now()
		return before, after
	}

	// btime
	bBefore, bAfter := withFile(os.O_CREATE|os.O_RDWR, func(file dos.File) {})

	time.Sleep(minDelta)

	// mtime
	mBefore, mAfter := withFile(os.O_RDWR, func(file dos.File) {
		_, err := io.WriteString(file, "#!/bin/sh\n")
		require.NoError(t, err)
	})

	time.Sleep(minDelta)

	// ctime
	cBefore := time.Now()
	time.Sleep(fsVsClockLeeway)
	require.NoError(t, os.Chmod(filename, 0o777))
	time.Sleep(fsVsClockLeeway)
	cAfter := time.Now()

	// stat
	var stat logging.SysInfo
	withFile(os.O_RDWR, func(file dos.File) {
		var err error
		stat, err = logging.FStat(file)
		require.NoError(t, err)
	})

	// validate
	assertInRange := func(before, after, x time.Time, msg string) {
		if x.Before(before) || x.After(after) {
			t.Errorf("%s: %v: not in range [%v, %v]", msg, x, before, after)
		} else {
			t.Logf("%s: %v", msg, x)
		}
	}

	if okIfBTimeIsCTime && stat.BirthTime() == stat.ChangeTime() {
		btimeIsCTime = true
		t.Logf("btime: %v (spoofed with ctime)", stat.BirthTime())
	} else {
		assertInRange(bBefore, bAfter, stat.BirthTime(), "btime")
	}
	assertInRange(mBefore, mAfter, stat.ModifyTime(), "mtime")
	if runtime.GOOS == "windows" {
		cBefore, cAfter = mBefore, mAfter
	}
	assertInRange(cBefore, cAfter, stat.ChangeTime(), "ctime")
	return btimeIsCTime
}
