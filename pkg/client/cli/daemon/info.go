package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

type Info struct {
	Options      map[string]string `json:"options,omitempty"`
	InDocker     bool              `json:"in_docker,omitempty"`
	Name         string            `json:"name,omitempty"`
	KubeContext  string            `json:"kube_context,omitempty"`
	Namespace    string            `json:"namespace,omitempty"`
	DaemonPort   int               `json:"daemon_port,omitempty"`
	ExposedPorts []string          `json:"exposed_ports,omitempty"`
	Hostname     string            `json:"hostname,omitempty"`
}

func (info *Info) DaemonID() *Identifier {
	id, _ := NewIdentifier(info.Name, info.KubeContext, info.Namespace, info.InDocker)
	return id
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
	return cache.SaveToUserCache(ctx, object, filepath.Join(daemonsDirName, file), cache.Public)
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

func WaitUntilAllVanishes(ctx context.Context, ttw time.Duration) error {
	giveUp := time.Now().Add(ttw)
	for giveUp.After(time.Now()) {
		files, err := infoFiles(ctx)
		if err != nil || len(files) == 0 {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
	return errors.New("timeout while waiting for daemon files to vanish")
}

func DeleteAllInfos(ctx context.Context) error {
	files, err := infoFiles(ctx)
	if err != nil {
		return err
	}
	for _, file := range files {
		_ = cache.DeleteFromUserCache(ctx, filepath.Join(daemonsDirName, file.Name()))
	}
	return nil
}

func LoadInfos(ctx context.Context) ([]*Info, error) {
	files, err := infoFiles(ctx)
	if err != nil {
		return nil, err
	}

	DaemonInfos := make([]*Info, len(files))
	for i, file := range files {
		if err = cache.LoadFromUserCache(ctx, &DaemonInfos[i], filepath.Join(daemonsDirName, file.Name())); err != nil {
			return nil, err
		}
	}
	return DaemonInfos, nil
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

type InfoMatchError string

func (i InfoMatchError) Error() string {
	return string(i)
}

type MultipleDaemonsError []*Info //nolint:errname // Don't want a plural name just because the type is a slice

func (m MultipleDaemonsError) Error() string {
	sb := strings.Builder{}
	sb.WriteString("multiple daemons are running, please select ")
	l := len(m)
	i := 0
	if l > 2 {
		sb.WriteString("one of ")
		for ; i+2 < l; i++ {
			sb.WriteString(m[i].DaemonID().Name)
			sb.WriteString(", ")
		}
	} else {
		sb.WriteString(m[i].DaemonID().Name)
		i++
	}
	sb.WriteString(" or ")
	sb.WriteString(m[i].DaemonID().Name)
	sb.WriteString(" using the --use <match> flag")
	return sb.String()
}

func LoadMatchingInfo(ctx context.Context, match *regexp.Regexp) (*Info, error) {
	if match == nil {
		infos, err := LoadInfos(ctx)
		if err != nil {
			return nil, err
		}
		switch len(infos) {
		case 0:
			return nil, os.ErrNotExist
		case 1:
			return infos[0], err
		default:
			return nil, MultipleDaemonsError(infos)
		}
	}
	files, err := infoFiles(ctx)
	if err != nil {
		return nil, err
	}
	var found string
	for _, file := range files {
		name := file.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		// If a match is given, then strip ".json" and apply it.
		if match.MatchString(name[:len(name)-5]) {
			if found != "" {
				return nil, errcat.User.New(
					InfoMatchError(fmt.Sprintf("the expression %q does not uniquely identify a running daemon", match.String())))
			}
			found = name
		}
	}
	if found == "" {
		return nil, os.ErrNotExist
	}
	return LoadInfo(ctx, found)
}

// CancelWhenRmFromCache watches for the file to be removed from the cache, then calls cancel.
func CancelWhenRmFromCache(ctx context.Context, cancel context.CancelFunc, filename string) error {
	return WatchInfos(ctx, func(ctx context.Context) error {
		exists, err := InfoExists(ctx, filename)
		if err != nil {
			return err
		}
		if !exists {
			// spec removed from cache, shut down gracefully
			dlog.Infof(ctx, "daemon file %s removed from cache, shutting down gracefully", filename)
			cancel()
		}
		return nil
	}, filename)
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
