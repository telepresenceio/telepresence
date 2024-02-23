package dos

import (
	"context"
	"io/fs"
	"os"

	"github.com/rogpeppe/go-internal/lockedfile"
)

type lockedFs struct {
	osFs
}

func WithLockedFs(ctx context.Context) context.Context {
	return WithFS(ctx, &lockedFs{osFs: newOS(ctx)})
}

func (fs *lockedFs) Create(name string) (File, error) {
	f, err := lockedfile.Create(name)
	if err != nil {
		// It's important to return a File nil here, not a File that represents an *os.File nil.
		return nil, err
	}
	return fs.chownFile(f)
}

func (*lockedFs) Open(name string) (File, error) {
	f, err := lockedfile.Open(name)
	if err != nil {
		// It's important to return a File nil here, not a File that represents an *os.File nil.
		return nil, err
	}
	return f, nil
}

func (fs *lockedFs) OpenFile(name string, flag int, perm fs.FileMode) (File, error) {
	if fs.mustChown() {
		if (flag & os.O_CREATE) == os.O_CREATE {
			if _, err := os.Stat(name); os.IsNotExist(err) {
				f, err := lockedfile.OpenFile(name, flag, perm)
				if err != nil {
					return nil, err
				}
				return fs.chownFile(f)
			}
		}
	}
	f, err := lockedfile.OpenFile(name, flag, perm)
	if err != nil {
		// It's important to return a File nil here, not a File that represents an *os.File nil.
		return nil, err
	}
	return f, nil
}
