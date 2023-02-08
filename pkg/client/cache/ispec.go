package cache

import (
	"context"
	"io/ioutil"
	"path/filepath"

	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

const ispecDirName = "ispec"

// TODO change object any to correct struct
func SaveToIspecCache(ctx context.Context, object any, file string) error {
	return SaveToUserCache(ctx, object, filepath.Join(ispecDirName, file))
}

func DeleteFromIspecCache(ctx context.Context, file string) error {
	return DeleteFromUserCache(ctx, filepath.Join(ispecDirName, file))
}

// TODO change object any to correct struct
func LoadIspecsFromCache(ctx context.Context, prefix string) ([]any, error) {
	dir, err := filelocation.AppUserCacheDir(ctx)
	if err != nil {
		return nil, err
	}
	ispecDir := filepath.Join(dir, ispecDirName)

	files, err := ioutil.ReadDir(ispecDir)
	if err != nil {
		return nil, err
	}

	ispecs := make([]any, len(files))
	for i, file := range files {
		loadFromFilePath(&ispecs[i], filepath.Join(ispecDir, file.Name()))
	}
	return ispecs, nil
}
