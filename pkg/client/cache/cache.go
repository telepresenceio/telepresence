package cache

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

type Permissions fs.FileMode

const (
	Public  Permissions = 0o644
	Private Permissions = 0o600
)

func SaveToUserCache(ctx context.Context, object any, file string, perm Permissions) error {
	ctx = dos.WithLockedFs(ctx)
	jsonContent, err := json.Marshal(object)
	if err != nil {
		return err
	}

	// add file path (ex. "ispec/00-00-0000.json")
	fullFilePath := filepath.Join(filelocation.AppUserCacheDir(ctx), file)
	// get dir of joined path
	dir := filepath.Dir(fullFilePath)
	if err := dos.MkdirAll(ctx, dir, 0o755); err != nil {
		return err
	}
	return writeFile(ctx, fullFilePath, jsonContent, fs.FileMode(perm), perm == Private)
}

// writeFile writes data without following a link at path. When private is set, the file is readable only by its
// owner also when it already exists with wider permissions; the mode is set before the new content is written.
func writeFile(ctx context.Context, path string, data []byte, perm fs.FileMode, private bool) error {
	f, err := dos.OpenFile(ctx, path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|noFollow, perm)
	if err != nil {
		return err
	}
	if c, ok := f.(interface{ Chmod(fs.FileMode) error }); private && ok {
		if err = c.Chmod(perm); err != nil {
			_ = f.Close()
			return err
		}
	}
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func LoadFromUserCache(ctx context.Context, dest any, file string) error {
	ctx = dos.WithLockedFs(ctx)
	path := filepath.Join(filelocation.AppUserCacheDir(ctx), file)
	jsonContent, err := dos.ReadFile(ctx, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(jsonContent, &dest); err != nil {
		return fmt.Errorf("failed to parse JSON from file %s: %w", path, err)
	}
	return nil
}

func DeleteFromUserCache(ctx context.Context, file string) error {
	ctx = dos.WithLockedFs(ctx)
	if err := dos.Remove(ctx, filepath.Join(filelocation.AppUserCacheDir(ctx), file)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func TouchUserCache(ctx context.Context, file string) error {
	now := time.Now()
	return os.Chtimes(filepath.Join(filelocation.AppUserCacheDir(ctx), file), now, now)
}

func UserCacheModTime(ctx context.Context, file string) (time.Time, error) {
	st, err := os.Stat(filepath.Join(filelocation.AppUserCacheDir(ctx), file))
	if err != nil {
		return time.Time{}, err
	}
	return st.ModTime(), nil
}

func ExistsInCache(ctx context.Context, fileName string) (bool, error) {
	ctx = dos.WithLockedFs(ctx)
	path := filepath.Join(filelocation.AppUserCacheDir(ctx), fileName)
	if _, err := dos.Stat(ctx, path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
