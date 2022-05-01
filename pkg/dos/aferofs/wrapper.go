package aferofs

import (
	"errors"
	"io/fs"
	"os"

	"github.com/spf13/afero"

	"github.com/telepresenceio/telepresence/v2/pkg/dos"
)

type wrapFs struct {
	afero.Fs
}

func Wrap(fs afero.Fs) dos.FileSystem {
	return wrapFs{fs}
}

func wrapFile(f afero.File, err error) (dos.File, error) {
	if err != nil {
		return nil, err
	}
	return file{f}, err
}

func (a wrapFs) Abs(name string) (string, error) {
	return "", errors.New("afero.Fs does not implement Abs")
}

func (a wrapFs) Chdir(path string) error {
	return errors.New("afero.Fs does not implement Chdir")
}

func (a wrapFs) Create(name string) (dos.File, error) {
	return wrapFile(a.Fs.Create(name))
}

func (a wrapFs) Getwd() (string, error) {
	return "", errors.New("afero.Fs does not implement Getwd")
}

func (a wrapFs) ReadDir(name string) ([]fs.DirEntry, error) {
	dir, err := a.Open(name)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return dir.ReadDir(0)
}

func (a wrapFs) ReadFile(name string) ([]byte, error) {
	return afero.ReadFile(a.Fs, name)
}

func (a wrapFs) RealPath(name string) (string, error) {
	if rp, ok := a.Fs.(interface{ RealPath(string) (string, error) }); ok {
		return rp.RealPath(name)
	}
	return "", errors.New("RealPath is not implemented")
}

func (a wrapFs) Open(name string) (dos.File, error) {
	return wrapFile(a.Fs.Open(name))
}

func (a wrapFs) OpenFile(name string, flag int, perm fs.FileMode) (dos.File, error) {
	return wrapFile(a.Fs.OpenFile(name, flag, perm))
}

func (a wrapFs) Symlink(oldName, newName string) error {
	if lfs, ok := a.Fs.(afero.Linker); ok {
		return lfs.SymlinkIfPossible(oldName, newName)
	}
	return &os.LinkError{Op: "symlink", Old: oldName, New: newName, Err: afero.ErrNoSymlink}
}

func (a wrapFs) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return afero.WriteFile(a.Fs, name, data, perm)
}

// The afero.File lacks ReadDir. Instances implement fs.ReadDirFile though
type file struct {
	afero.File
}

// dirEntry provides adapter from os.FileInfo to fs.DirEntry
type dirEntry struct {
	fs.FileInfo
}

func (d dirEntry) Type() fs.FileMode { return d.FileInfo.Mode().Type() }

func (d dirEntry) Info() (fs.FileInfo, error) { return d.FileInfo, nil }

func (a file) ReadDir(count int) ([]fs.DirEntry, error) {
	if d, ok := a.File.(fs.ReadDirFile); ok {
		return d.ReadDir(count)
	}
	fis, err := a.File.Readdir(count) //nolint:forbidigo // this is not an os.File
	if err != nil {
		return nil, err
	}
	des := make([]fs.DirEntry, len(fis))
	for i, fi := range fis {
		des[i] = dirEntry{fi}
	}
	return des, nil
}
