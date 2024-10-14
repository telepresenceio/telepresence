package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func InstallID(ctx context.Context) (string, error) {
	idFile := filepath.Join(filelocation.AppUserConfigDir(ctx), "id")
	data, err := dos.ReadFile(ctx, idFile)
	switch {
	case err == nil:
		return strings.TrimSpace(string(data)), nil
	case os.IsNotExist(err):
		id := uuid.New().String()
		if err = dos.WriteFile(ctx, idFile, []byte(id), 0o644); err != nil {
			return "", err
		}
		return id, nil
	default:
		return "", err
	}
}
