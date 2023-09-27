package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

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
	return dos.WriteFile(ctx, fullFilePath, jsonContent, (fs.FileMode(perm)))
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
	if err := dos.Remove(ctx, filepath.Join(filelocation.AppUserCacheDir(ctx), file)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
