package cache

import (
	"context"
	"os"
	"path/filepath"

	"github.com/datawire/telepresence-pro/pkg/ispec"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

const ispecDirName = "ispec"

func SaveToIspecCache(ctx context.Context, object *ispec.InterceptSpecification, file string) error {
	return cache.SaveToUserCache(ctx, object, filepath.Join(ispecDirName, file))
}

func DeleteFromIspecCache(ctx context.Context, file string) error {
	return cache.DeleteFromUserCache(ctx, filepath.Join(ispecDirName, file))
}

func LoadIspecsFromCache(ctx context.Context) ([]*ispec.InterceptSpecification, error) {
	dir, err := filelocation.AppUserCacheDir(ctx)
	if err != nil {
		return nil, err
	}
	ispecDir := filepath.Join(dir, ispecDirName)

	files, err := os.ReadDir(ispecDir)
	if err != nil {
		return nil, err
	}

	ispecs := make([]*ispec.InterceptSpecification, len(files))
	for i, file := range files {
		path := filepath.Join(ispecDirName, file.Name())
		err := cache.LoadFromUserCache(ctx, &ispecs[i], path)
		if err != nil {
			return nil, err
		}
	}
	return ispecs, nil
}
