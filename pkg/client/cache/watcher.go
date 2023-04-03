package cache

import (
	"context"
	"math"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

// WatchUserCache uses a file system watcher that receives events when the file changes
// and calls the given function when that happens.
func WatchUserCache(ctx context.Context, subdir string, onChange func(context.Context) error, files ...string) error {
	dir := filepath.Join(filelocation.AppUserCacheDir(ctx), subdir)

	// Ensure that the user cache directory exists.
	if err := dos.MkdirAll(ctx, dir, 0o755); err != nil {
		return err
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	// The directory containing the files must be watched because editing a
	// file will typically end with renaming the original and then creating
	// a new file. A watcher that follows the inode will not see when the new
	// file is created.
	if err = watcher.Add(dir); err != nil {
		return err
	}

	// The delay timer will initially sleep forever. It's reset to a very short
	// delay when the file is modified.
	delay := time.AfterFunc(time.Duration(math.MaxInt64), func() {
		if err := onChange(ctx); err != nil {
			dlog.Error(ctx, err)
		}
	})
	defer delay.Stop()

	for i := range files {
		files[i] = filepath.Join(dir, files[i])
	}
	isOfInterest := func(s string) bool {
		for _, file := range files {
			if s == file {
				return true
			}
		}
		return false
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err = <-watcher.Errors:
			dlog.Error(ctx, err)
		case event := <-watcher.Events:
			if event.Op&(fsnotify.Remove|fsnotify.Write|fsnotify.Create) != 0 && isOfInterest(event.Name) {
				// The file was created, modified, or removed. Let's defer the call to onChange just
				// a little bit in case there are more modifications to it.
				delay.Reset(5 * time.Millisecond)
			}
		}
	}
}
