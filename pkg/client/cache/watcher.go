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

// WatchUserCache uses a file system watcher that receives events when one of the given files changes
// and calls the given function when that happens.
// All files in the given subDir are watched when the list of files is empty.
func WatchUserCache(ctx context.Context, subDir string, onChange func(context.Context) error, files ...string) error {
	dir := filepath.Join(filelocation.AppUserCacheDir(ctx), subDir)

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
		select {
		case <-ctx.Done():
			return
		default:
			if err := onChange(ctx); err != nil {
				dlog.Error(ctx, err)
			}
		}
	})
	defer delay.Stop()

	isOfInterest := func(string) bool { return true }
	if len(files) > 0 {
		for i := range files {
			files[i] = filepath.Join(dir, files[i])
		}
		isOfInterest = func(s string) bool {
			for _, file := range files {
				if s == file {
					return true
				}
			}
			return false
		}
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
