package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func SaveToUserCache(ctx context.Context, object any, file string) error {
	jsonContent, err := json.Marshal(object)
	if err != nil {
		return err
	}

	cacheDir, err := filelocation.AppUserCacheDir(ctx)
	if err != nil {
		return err
	}
	fullFilePath := filepath.Join(cacheDir, file)
	dir := filepath.Dir(fullFilePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(fullFilePath, jsonContent, 0o600)
}

func LoadFromUserCache(ctx context.Context, dest any, file string) error {
	dir, err := filelocation.AppUserCacheDir(ctx)
	if err != nil {
		return err
	}
	filePath := filepath.Join(dir, file)
	return loadFromFilePath(dest, filePath)
}

func loadFromFilePath(dest any, filePath string) error {
	jsonContent, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(jsonContent, &dest); err != nil {
		return fmt.Errorf("failed to parse JSON from file %s: %w", filePath, err)
	}
	return nil
}

func DeleteFromUserCache(ctx context.Context, file string) error {
	dir, err := filelocation.AppUserCacheDir(ctx)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, file)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
