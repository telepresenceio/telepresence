package daemon

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

type Info struct {
	Options     map[string]string `json:"options,omitempty"`
	InDocker    bool              `json:"in_docker,omitempty"`
	KubeContext string            `json:"kube_context,omitempty"`
	DaemonPort  int               `json:"daemon_port,omitempty"`
}

const (
	daemonsDirName    = "daemons"
	keepAliveInterval = 5 * time.Second
)

func LoadInfo(ctx context.Context, file string) (*Info, error) {
	var di Info
	if err := cache.LoadFromUserCache(ctx, &di, filepath.Join(daemonsDirName, file)); err != nil {
		return nil, err
	}
	return &di, nil
}

func SaveInfo(ctx context.Context, object *Info, file string) error {
	return cache.SaveToUserCache(ctx, object, filepath.Join(daemonsDirName, file))
}

func DeleteInfo(ctx context.Context, file string) error {
	return cache.DeleteFromUserCache(ctx, filepath.Join(daemonsDirName, file))
}

func InfoExists(ctx context.Context, file string) (bool, error) {
	return cache.ExistsInCache(ctx, filepath.Join(daemonsDirName, file))
}

func WatchInfos(ctx context.Context, onChange func(context.Context) error, files ...string) error {
	return cache.WatchUserCache(ctx, daemonsDirName, onChange, files...)
}

func LoadInfos(ctx context.Context) ([]*Info, error) {
	files, err := infoFiles(ctx)
	if err != nil {
		return nil, err
	}

	infos := make([]*Info, len(files))
	for i, file := range files {
		if err = cache.LoadFromUserCache(ctx, &infos[i], filepath.Join(daemonsDirName, file.Name())); err != nil {
			return nil, err
		}
	}
	return infos, nil
}

func infoFiles(ctx context.Context) ([]fs.DirEntry, error) {
	files, err := os.ReadDir(filepath.Join(filelocation.AppUserCacheDir(ctx), daemonsDirName))
	if err != nil {
		if os.IsNotExist(err) {
			err = nil
		}
		return nil, err
	}
	active := make([]fs.DirEntry, 0, len(files))
	for _, file := range files {
		fi, err := file.Info()
		if err != nil {
			return nil, err
		}
		age := time.Since(fi.ModTime())
		if age > keepAliveInterval+600*time.Millisecond {
			// File has gone stale
			dlog.Debugf(ctx, "Deleting stale info %s with age = %s", file.Name(), age)
			if err = cache.DeleteFromUserCache(ctx, filepath.Join(daemonsDirName, file.Name())); err != nil {
				return nil, err
			}
		} else {
			active = append(active, file)
		}
	}
	return active, err
}

var (
	diNameRx   = regexp.MustCompile(`^(.+?)-(\d+)\.json$`)
	pathNameRx = regexp.MustCompile(`[^a-zA-Z0-9-_\.]`)
)

func PortForName(ctx context.Context, context string) (int, error) {
	context = pathNameRx.ReplaceAllString(context, "-")
	files, err := infoFiles(ctx)
	if err != nil {
		return 0, err
	}
	for _, file := range files {
		if m := diNameRx.FindStringSubmatch(file.Name()); m != nil && m[1] == context {
			port, _ := strconv.Atoi(m[2])
			return port, nil
		}
	}
	return 0, os.ErrNotExist
}

func InfoFile(name string, port int) string {
	fileName := fmt.Sprintf("%s-%d.json", name, port)
	return pathNameRx.ReplaceAllString(fileName, "-")
}

// KeepInfoAlive updates the access and modification times of the given Info
// periodically so that it never gets older than keepAliveInterval. This means that
// any file with a modification time older than the current time minus two keepAliveIntervals
// can be considered stale and should be removed.
//
// The alive poll ends and the Info is deleted when the context is cancelled.
func KeepInfoAlive(ctx context.Context, file string) error {
	daemonFile := filepath.Join(filelocation.AppUserCacheDir(ctx), daemonsDirName, file)
	ticker := time.NewTicker(keepAliveInterval)
	defer ticker.Stop()
	now := time.Now()
	for {
		if err := os.Chtimes(daemonFile, now, now); err != nil {
			if os.IsNotExist(err) {
				// File is removed, so stop trying to update its timestamps
				dlog.Debugf(ctx, "Daemon info %s does not exist", file)
				return nil
			}
			return fmt.Errorf("failed to update timestamp on %s: %w", daemonFile, err)
		}
		select {
		case <-ctx.Done():
			dlog.Debugf(ctx, "Deleting daemon info %s because context was cancelled", file)
			_ = DeleteInfo(ctx, file)
			return nil
		case now = <-ticker.C:
		}
	}
}
