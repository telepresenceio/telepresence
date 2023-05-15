package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func SaveToUserCache(ctx context.Context, object any, file string) error {
	jsonContent, err := json.Marshal(object)
	if err != nil {
		return err
	}

	// add file path (ex. "ispec/00-00-0000.json")
	fullFilePath := filepath.Join(filelocation.AppUserCacheDir(ctx), file)
	// get dir of joined path
	dir := filepath.Dir(fullFilePath)
	if err := dos.MkdirAll(ctx, dir, 0o700); err != nil {
		return err
	}
	return dos.WriteFile(ctx, fullFilePath, jsonContent, 0o600)
}

func LoadFromUserCache(ctx context.Context, dest any, file string) error {
	path := filepath.Join(filelocation.AppUserCacheDir(ctx), file)
	jsonContent, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(jsonContent, &dest); err != nil {
		return fmt.Errorf("failed to parse JSON from file %s: %w", path, err)
	}
	return nil
}

func DeleteFromUserCache(ctx context.Context, file string) error {
	if err := os.Remove(filepath.Join(filelocation.AppUserCacheDir(ctx), file)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func ExistsInCache(ctx context.Context, fileName string) (bool, error) {
	path := filepath.Join(filelocation.AppUserCacheDir(ctx), fileName)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
