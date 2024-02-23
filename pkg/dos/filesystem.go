package dos

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall" //nolint:depguard // "unix" don't work on windows
)

// File represents a file in the filesystem. The os.File struct implements this interface.
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

type OwnedFile interface {
	Chown(uid, gid int) error
}

// FileSystem is an interface that implements functions in the os package.
type FileSystem interface {
	Abs(name string) (string, error)
	Chdir(name string) error
	Create(name string) (File, error)
	Getwd() (string, error)
	Mkdir(name string, perm fs.FileMode) error
	MkdirAll(name string, perm fs.FileMode) error
	Open(name string) (File, error)
	OpenFile(name string, flag int, perm fs.FileMode) (File, error)
	ReadDir(name string) ([]fs.DirEntry, error)
	ReadFile(name string) ([]byte, error)
	RealPath(name string) (string, error)
	Remove(name string) error
	RemoveAll(name string) error
	Rename(oldName, newName string) error
	Stat(name string) (fs.FileInfo, error)
	Symlink(oldName, newName string) error
	WriteFile(name string, data []byte, perm fs.FileMode) error
}

type osFs struct {
	tpUID int
	tpGID int
}

func (*osFs) Abs(name string) (string, error) {
	return filepath.Abs(name)
}

func (*osFs) Chdir(name string) error {
	return os.Chdir(name)
}

func (fs *osFs) Create(name string) (File, error) {
	f, err := os.Create(name)
	if err != nil {
		// It's important to return a File nil here, not a File that represents an *os.File nil.
		return nil, err
	}
	return fs.chownFile(f)
}

func (*osFs) Getwd() (string, error) {
	return os.Getwd()
}

func (fs *osFs) Mkdir(name string, perm fs.FileMode) error {
	return fs.chown(os.Mkdir(name, perm), name)
}

// MkdirAll is a slightly modified version the same function in of Go 1.19.3's os/path.go.
func (fs *osFs) MkdirAll(path string, perm fs.FileMode) error {
	// Fast path: if we can tell whether path is a directory or file, stop with success or error.
	dir, err := os.Stat(path)
	if err == nil {
		if dir.IsDir() {
			return nil
		}
		return &os.PathError{Op: "mkdir", Path: path, Err: syscall.ENOTDIR} //nolint:forbidigo // we want the same error
	}

	// Slow path: make sure parent exists and then call Mkdir for path.
	i := len(path)
	for i > 0 && os.IsPathSeparator(path[i-1]) { // Skip trailing path separator.
		i--
	}

	j := i
	for j > 0 && !os.IsPathSeparator(path[j-1]) { // Scan backward over element.
		j--
	}

	if j > 1 {
		// Create parent.
		if err = fs.MkdirAll(path[:j-1], perm); err != nil {
			return err
		}
	}

	// Parent now exists; invoke Mkdir and use its result.
	err = fs.Mkdir(path, perm)
	if err != nil {
		// Handle arguments like "foo/." by
		// double-checking that directory doesn't exist.
		dir, err1 := os.Lstat(path)
		if err1 == nil && dir.IsDir() {
			return nil
		}
		return err
	}
	return nil
}

func (*osFs) Open(name string) (File, error) {
	f, err := os.Open(name)
	if err != nil {
		// It's important to return a File nil here, not a File that represents an *os.File nil.
		return nil, err
	}
	return f, nil
}

func (fs *osFs) OpenFile(name string, flag int, perm fs.FileMode) (File, error) {
	if fs.mustChown() {
		if (flag & os.O_CREATE) == os.O_CREATE {
			if _, err := os.Stat(name); os.IsNotExist(err) {
				f, err := os.OpenFile(name, flag, perm)
				if err != nil {
					return nil, err
				}
				return fs.chownFile(f)
			}
		}
	}
	f, err := os.OpenFile(name, flag, perm)
	if err != nil {
		// It's important to return a File nil here, not a File that represents an *os.File nil.
		return nil, err
	}
	return f, nil
}

func (*osFs) ReadDir(name string) ([]fs.DirEntry, error) {
	return os.ReadDir(name)
}

func (*osFs) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(name)
}

func (*osFs) RealPath(name string) (string, error) {
	return filepath.Abs(name)
}

func (*osFs) Remove(name string) error {
	return os.Remove(name)
}

func (*osFs) RemoveAll(name string) error {
	return os.RemoveAll(name)
}

func (*osFs) Rename(oldName, newName string) error {
	return os.Rename(oldName, newName)
}

func (*osFs) Stat(name string) (fs.FileInfo, error) {
	return os.Stat(name)
}

func (*osFs) Symlink(oldName, newName string) error {
	return os.Symlink(oldName, newName)
}

func (fs *osFs) WriteFile(name string, data []byte, perm fs.FileMode) error {
	if fs.mustChown() {
		if _, err := os.Stat(name); os.IsNotExist(err) {
			return fs.chown(os.WriteFile(name, data, perm), name)
		}
	}
	return os.WriteFile(name, data, perm)
}

func (fs *osFs) mustChown() bool {
	return fs.tpUID > 0 || fs.tpGID > 0
}

func (fs *osFs) chown(err error, name string) error {
	if err == nil && fs.mustChown() {
		err = os.Chown(name, fs.tpUID, fs.tpGID)
	}
	return err
}

func (fs *osFs) chownFile(f File) (File, error) {
	if fs.mustChown() {
		var err error
		if of, ok := f.(OwnedFile); ok {
			err = of.Chown(fs.tpUID, fs.tpGID)
		} else {
			err = fmt.Errorf("chown is not supported by %T", f)
		}
		if err != nil {
			_ = f.Close()
			_ = fs.Remove(f.Name())
			return nil, err
		}
	}
	return f, nil
}

type fsKey struct{}

// WithFS assigns the FileSystem to be used by subsequent file system related dos functions.
func WithFS(ctx context.Context, fs FileSystem) context.Context {
	return context.WithValue(ctx, fsKey{}, fs)
}

func getFS(ctx context.Context) FileSystem {
	if f, ok := ctx.Value(fsKey{}).(FileSystem); ok {
		return f
	}
	of := newOS(ctx)
	return &of
}

func newOS(ctx context.Context) osFs {
	of := osFs{}
	if env, ok := LookupEnv(ctx, "TELEPRESENCE_UID"); ok {
		of.tpUID, _ = strconv.Atoi(env)
	}
	if env, ok := LookupEnv(ctx, "TELEPRESENCE_GID"); ok {
		of.tpGID, _ = strconv.Atoi(env)
	}
	return of
}

// Abs is like filepath.Abs but delegates to the context's FS.
func Abs(ctx context.Context, name string) (string, error) {
	return getFS(ctx).Abs(name)
}

// Chdir is like os.Chdir but delegates to the context's FS.
func Chdir(ctx context.Context, path string) error {
	return getFS(ctx).Chdir(path)
}

// Create is like os.Create but delegates to the context's FS.
func Create(ctx context.Context, name string) (File, error) {
	return getFS(ctx).Create(name)
}

// Getwd is like os.Getwd but delegates to the context's FS.
func Getwd(ctx context.Context) (string, error) {
	return getFS(ctx).Getwd()
}

// Mkdir is like os.Mkdir but delegates to the context's FS.
func Mkdir(ctx context.Context, name string, perm fs.FileMode) error {
	return getFS(ctx).Mkdir(name, perm)
}

// MkdirAll is like os.MkdirAll but delegates to the context's FS.
func MkdirAll(ctx context.Context, name string, perm fs.FileMode) error {
	return getFS(ctx).MkdirAll(name, perm)
}

// Open is like os.Open but delegates to the context's FS.
func Open(ctx context.Context, name string) (File, error) {
	return getFS(ctx).Open(name)
}

// OpenFile is like os.OpenFile but delegates to the context's FS.
func OpenFile(ctx context.Context, name string, flag int, perm fs.FileMode) (File, error) {
	return getFS(ctx).OpenFile(name, flag, perm)
}

// ReadDir is like os.ReadDir but delegates to the context's FS.
func ReadDir(ctx context.Context, name string) ([]fs.DirEntry, error) {
	return getFS(ctx).ReadDir(name)
}

// ReadFile is like os.ReadFile but delegates to the context's FS.
func ReadFile(ctx context.Context, name string) ([]byte, error) { // MODIFIED
	return getFS(ctx).ReadFile(name)
}

// RealPath returns the real path in the underlying os filesystem or
// an error if there's no os filesystem.
func RealPath(ctx context.Context, name string) (string, error) {
	return getFS(ctx).RealPath(name)
}

// Remove is like os.Remove but delegates to the context's FS.
func Remove(ctx context.Context, name string) error {
	return getFS(ctx).Remove(name)
}

// RemoveAll is like os.RemoveAll but delegates to the context's FS.
func RemoveAll(ctx context.Context, name string) error {
	return getFS(ctx).RemoveAll(name)
}

// Rename is like os.Rename but delegates to the context's FS.
func Rename(ctx context.Context, oldName, newName string) error {
	return getFS(ctx).Rename(oldName, newName)
}

func WriteFile(ctx context.Context, name string, data []byte, perm fs.FileMode) error {
	return getFS(ctx).WriteFile(name, data, perm)
}

// Stat is like os.Stat but delegates to the context's FS.
func Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	return getFS(ctx).Stat(name)
}

// Symlink is like os.Symlink but delegates to the context's FS.
func Symlink(ctx context.Context, oldName, newName string) error {
	return getFS(ctx).Symlink(oldName, newName)
}
