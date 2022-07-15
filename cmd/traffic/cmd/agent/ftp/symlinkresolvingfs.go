package ftp

import (
	"io/fs"
	"path/filepath"

	"github.com/spf13/afero"
)

// SymLinkResolvingFs returns an afero file system that resolves all symlinks so
// that they become invisible.
func SymLinkResolvingFs(fs afero.Fs) afero.Fs {
	return &symlinkResolvingFs{Fs: fs}
}

// symlinkResolvingFs will always return a symLinkResolvingFile on Create, Open,
// and OpenFile.
type symlinkResolvingFs struct {
	afero.Fs
}

// symlinkResolvingFile will replace symlinks returned by Readdir() with the
// resolved result
type symlinkResolvingFile struct {
	afero.File
	fs *symlinkResolvingFs
}

func (h *symlinkResolvingFs) Create(name string) (afero.File, error) {
	f, err := h.Fs.Create(name)
	if err == nil {
		f = &symlinkResolvingFile{File: f, fs: h}
	}
	return f, err
}

func (h *symlinkResolvingFs) Open(name string) (afero.File, error) {
	f, err := h.Fs.Open(name)
	if err == nil {
		f = &symlinkResolvingFile{File: f, fs: h}
	}
	return f, err
}

func (h *symlinkResolvingFs) OpenFile(name string, flag int, perm fs.FileMode) (afero.File, error) {
	f, err := h.Fs.OpenFile(name, flag, perm)
	if err == nil {
		f = &symlinkResolvingFile{File: f, fs: h}
	}
	return f, err
}

func (h *symlinkResolvingFile) Readdir(count int) ([]fs.FileInfo, error) {
	fis, err := h.File.Readdir(count) //nolint:forbidigo // We actually reimplement this method
	if err != nil {
		return nil, err
	}
	for i, fi := range fis {
		if (fi.Mode() & fs.ModeSymlink) != 0 {
			// replace with resolved FileInfo from Stat()
			if fis[i], err = h.fs.Stat(filepath.Join(h.Name(), fi.Name())); err != nil {
				return nil, err
			}
		}
	}
	return fis, nil
}
