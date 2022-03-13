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
	Create(name string) (File, error)
	MkdirAll(path string, perm fs.FileMode) error
	Open(name string) (File, error)
	OpenFile(name string, flag int, perm fs.FileMode) (File, error)
	Stat(name string) (fs.FileInfo, error)
	Symlink(oldName, newName string) error
}

type osFs struct{}

func (osFs) Create(name string) (File, error) {
	return os.Create(name)
}

func (osFs) MkdirAll(path string, perm fs.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (osFs) Open(name string) (File, error) {
	return os.Open(name)
}

func (osFs) OpenFile(name string, flag int, perm fs.FileMode) (File, error) {
	return os.OpenFile(name, flag, perm)
}

func (osFs) Stat(name string) (fs.FileInfo, error) {
	return os.Stat(name)
}

func (osFs) Symlink(oldName, newName string) error {
	return os.Symlink(oldName, newName)
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

// Create is like os.Create but delegates to the context's FS
func Create(ctx context.Context, name string) (File, error) {
	return getFS(ctx).Create(name)
}

// MkdirAll is like os.MkdirAll but delegates to the context's FS
func MkdirAll(ctx context.Context, path string, perm fs.FileMode) error {
	return getFS(ctx).MkdirAll(path, perm)
}

// Open is like os.Open but delegates to the context's FS
func Open(ctx context.Context, name string) (File, error) {
	return getFS(ctx).Open(name)
}

// OpenFile is like os.OpenFile but delegates to the context's FS
func OpenFile(ctx context.Context, name string, flag int, perm fs.FileMode) (File, error) {
	return getFS(ctx).OpenFile(name, flag, perm)
}

// Stat is like os.Stat but delegates to the context's FS
func Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	return getFS(ctx).Stat(name)
}

// Symlink is like os.Symlink but delegates to the context's FS
func Symlink(ctx context.Context, oldName, newName string) error {
	return getFS(ctx).Symlink(oldName, newName)
}

// ReadFile is like os.ReadFile but delegates to the context's FS
// This function is a verbatim copy of Golang 1.17.6 os.ReadFile in src/os/file.go,
// except for lines marked "MODIFIED".
func ReadFile(ctx context.Context, name string) ([]byte, error) { // MODIFIED
	f, err := Open(ctx, name) // MODIFIED
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var size int
	if info, err := f.Stat(); err == nil {
		size64 := info.Size()
		if int64(int(size64)) == size64 {
			size = int(size64)
		}
	}
	size++ // one byte for final read at EOF

	// If a file claims a small size, read at least 512 bytes.
	// In particular, files in Linux's /proc claim size 0 but
	// then do not work right if read in small pieces,
	// so an initial read of 1 byte would not work correctly.
	if size < 512 {
		size = 512
	}

	data := make([]byte, 0, size)
	for {
		if len(data) >= cap(data) {
			d := data[:cap(data)] // MODIFIED split in two lines to satisfy gocritic lint complaint
			d = append(d, 0)      // MODIFIED
			data = d[:len(data)]
		}
		n, err := f.Read(data[len(data):cap(data)])
		data = data[:len(data)+n]
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return data, err
		}
	}
}
