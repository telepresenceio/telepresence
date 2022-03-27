package dos

import (
	"context"
	"io"
	"io/fs"
	"os"
)

// File represents a file in the filesystem. The os.File struct implements this interface
type File interface {
	io.Closer
	io.Reader
	io.ReaderAt
	io.Seeker
	io.Writer
	io.WriterAt

	Name() string
	Readdir(count int) ([]fs.FileInfo, error)
	Readdirnames(n int) ([]string, error)
	Stat() (fs.FileInfo, error)
	Sync() error
	Truncate(size int64) error
	WriteString(s string) (ret int, err error)
	ReadDir(count int) ([]fs.DirEntry, error)
}

// FileSystem is an interface that implements functions in the os package
type FileSystem interface {
	Chdir(name string) error
	Create(name string) (File, error)
	Getwd() (string, error)
	Mkdir(name string, perm fs.FileMode) error
	MkdirAll(name string, perm fs.FileMode) error
	Open(name string) (File, error)
	OpenFile(name string, flag int, perm fs.FileMode) (File, error)
	ReadDir(name string) ([]fs.DirEntry, error)
	ReadFile(name string) ([]byte, error)
	Remove(name string) error
	Stat(name string) (fs.FileInfo, error)
	Symlink(oldName, newName string) error
	WriteFile(name string, data []byte, perm fs.FileMode) error
}

type osFs struct{}

func (osFs) Chdir(path string) error {
	return os.Chdir(path)
}

func (osFs) Create(name string) (File, error) {
	return os.Create(name)
}

func (osFs) Getwd() (string, error) {
	return os.Getwd()
}

func (osFs) Mkdir(name string, perm fs.FileMode) error {
	return os.Mkdir(name, perm)
}

func (osFs) MkdirAll(name string, perm fs.FileMode) error {
	return os.MkdirAll(name, perm)
}

func (osFs) Open(name string) (File, error) {
	return os.Open(name)
}

func (osFs) OpenFile(name string, flag int, perm fs.FileMode) (File, error) {
	return os.OpenFile(name, flag, perm)
}

func (osFs) ReadDir(name string) ([]fs.DirEntry, error) {
	return os.ReadDir(name)
}

func (osFs) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}

func (osFs) Remove(name string) error {
	return os.Remove(name)
}

func (osFs) Stat(name string) (fs.FileInfo, error) {
	return os.Stat(name)
}

func (osFs) Symlink(oldName, newName string) error {
	return os.Symlink(oldName, newName)
}

func (osFs) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(name, data, perm)
}

type fsKey struct{}

// WithFS assigns the FileSystem to be used by subsequent file system related dos functions
func WithFS(ctx context.Context, fs FileSystem) context.Context {
	return context.WithValue(ctx, fsKey{}, fs)
}

func getFS(ctx context.Context) FileSystem {
	if fs, ok := ctx.Value(fsKey{}).(FileSystem); ok {
		return fs
	}
	return osFs{}
}

// Chdir is like os.Chdir but delegates to the context's FS
func Chdir(ctx context.Context, path string) error {
	return getFS(ctx).Chdir(path)
}

// Create is like os.Create but delegates to the context's FS
func Create(ctx context.Context, name string) (File, error) {
	return getFS(ctx).Create(name)
}

// Getwd is like os.Getwd but delegates to the context's FS
func Getwd(ctx context.Context) (string, error) {
	return getFS(ctx).Getwd()
}

// Mkdir is like os.Mkdir but delegates to the context's FS
func Mkdir(ctx context.Context, name string, perm fs.FileMode) error {
	return getFS(ctx).Mkdir(name, perm)
}

// MkdirAll is like os.MkdirAll but delegates to the context's FS
func MkdirAll(ctx context.Context, name string, perm fs.FileMode) error {
	return getFS(ctx).MkdirAll(name, perm)
}

// Open is like os.Open but delegates to the context's FS
func Open(ctx context.Context, name string) (File, error) {
	return getFS(ctx).Open(name)
}

// OpenFile is like os.OpenFile but delegates to the context's FS
func OpenFile(ctx context.Context, name string, flag int, perm fs.FileMode) (File, error) {
	return getFS(ctx).OpenFile(name, flag, perm)
}

// ReadDir is like os.ReadDir but delegates to the context's FS
func ReadDir(ctx context.Context, name string) ([]fs.DirEntry, error) {
	return getFS(ctx).ReadDir(name)
}

// ReadFile is like os.ReadFile but delegates to the context's FS
func ReadFile(ctx context.Context, name string) ([]byte, error) { // MODIFIED
	return getFS(ctx).ReadFile(name)
}

// Remove is like os.ReadDir but delegates to the context's FS
func Remove(ctx context.Context, name string) error {
	return getFS(ctx).Remove(name)
}

func WriteFile(ctx context.Context, name string, data []byte, perm fs.FileMode) error {
	return getFS(ctx).WriteFile(name, data, perm)
}

// Stat is like os.Stat but delegates to the context's FS
func Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	return getFS(ctx).Stat(name)
}

// Symlink is like os.Symlink but delegates to the context's FS
func Symlink(ctx context.Context, oldName, newName string) error {
	return getFS(ctx).Symlink(oldName, newName)
}
