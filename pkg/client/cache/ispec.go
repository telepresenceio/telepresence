package cache

import (
	"context"
	"os"
	"path/filepath"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

const ispecDirName = "ispec"

func SaveToIspecCache(ctx context.Context, object any, file string) error {
	return cache.SaveToUserCache(ctx, object, filepath.Join(ispecDirName, file))
}

func DeleteFromIspecCache(ctx context.Context, file string) error {
	return cache.DeleteFromUserCache(ctx, filepath.Join(ispecDirName, file))
}

func ExistsInIspecCache(ctx context.Context, file string) (bool, error) {
	return cache.ExistsInCache(ctx, filepath.Join(ispecDirName, file))
}

func WatchInIspecCache(ctx context.Context, onChange func(context.Context) error, files ...string) error {
	return WatchUserCache(ctx, ispecDirName, onChange, files...)
}

func LoadIspecsFromCache[T any](ctx context.Context) ([]T, error) {
	dir, err := filelocation.AppUserCacheDir(ctx)
	if err != nil {
		return nil, err
	}
	ispecDir := filepath.Join(dir, ispecDirName)

	files, err := os.ReadDir(ispecDir)
	if err != nil {
		return nil, err
	}

	ispecs := make([]T, len(files))
	for i, file := range files {
		path := filepath.Join(ispecDirName, file.Name())
		err := cache.LoadFromUserCache(ctx, &ispecs[i], path)
		if err != nil {
			return nil, err
		}
	}
	return ispecs, nil
}
