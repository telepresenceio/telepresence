package dos

import (
	"errors"
	"io/fs"
	"path/filepath"
)

type wdWrapper struct {
	base FileSystem
	wd   string
}

func WorkingDirWrapper(f FileSystem) FileSystem {
	return &wdWrapper{base: f, wd: "/"}
}

func (w *wdWrapper) basePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Clean(filepath.Join(w.wd, path))
}

func (w *wdWrapper) Chdir(path string) error {
	path = w.basePath(path)
	if s, err := w.base.Stat(path); err != nil {
		return err
	} else if !s.IsDir() {
		return errors.New("not a directory")
	}
	w.wd = path
	return nil
}

func (w *wdWrapper) Create(name string) (File, error) {
	return w.base.Create(w.basePath(name))
}

func (w *wdWrapper) Getwd() (string, error) {
	return w.wd, nil
}

func (w *wdWrapper) Mkdir(name string, perm fs.FileMode) error {
	return w.base.Mkdir(w.basePath(name), perm)
}

func (w *wdWrapper) MkdirAll(name string, perm fs.FileMode) error {
	return w.base.MkdirAll(w.basePath(name), perm)
}

func (w *wdWrapper) Open(name string) (File, error) {
	return w.base.Open(w.basePath(name))
}

func (w *wdWrapper) OpenFile(name string, flag int, perm fs.FileMode) (File, error) {
	return w.base.OpenFile(w.basePath(name), flag, perm)
}

func (w *wdWrapper) ReadDir(name string) ([]fs.DirEntry, error) {
	return w.base.ReadDir(w.basePath(name))
}

func (w *wdWrapper) ReadFile(name string) ([]byte, error) {
	return w.base.ReadFile(w.basePath(name))
}

func (w *wdWrapper) Remove(name string) error {
	return w.base.Remove(w.basePath(name))
}

func (w *wdWrapper) Stat(name string) (fs.FileInfo, error) {
	return w.base.Stat(w.basePath(name))
}

func (w *wdWrapper) Symlink(oldName, newName string) error {
	return w.base.Symlink(w.basePath(oldName), w.basePath(newName))
}

func (w *wdWrapper) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return w.base.WriteFile(w.basePath(name), data, perm)
}
