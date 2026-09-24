// Package sftpserver implements an SFTP server confined to the exports tree plus the allowed
// roots its exported symlinks may lead into. Client paths are walked one component at a time
// under os.Root; an absolute symlink is redirected onto the allowed root that claims its target,
// and everything past that redirect is resolved with a single os.Root call.
package sftpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strings"
	"syscall" //nolint:depguard // "unix" don't work on windows
	"time"

	"github.com/pkg/sftp"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
)

// maxRedirectHops bounds the number of allowed-root redirects a single leaf resolution will
// chase before giving up, matching the symlink-loop limit os/root.go applies internally.
const maxRedirectHops = 8

// allowedRoot pairs an open *os.Root with the cleaned form of the directory it was opened
// on, so a symlink's literal target text can be matched against it without ever walking the
// filesystem to do so.
type allowedRoot struct {
	dir  string
	root *os.Root
}

// Server serves SFTP connections confined to the exports directory tree passed to New plus
// whatever allowed roots its exported symlinks may lead into. All connections a single
// Server hands to Serve share its os.Root values and can therefore be served concurrently.
type Server struct {
	exports *os.Root
	allowed []allowedRoot
}

// New opens exportsDir and every entry of linkRoots as an os.Root and returns a Server that
// confines every request to the exports tree, redirecting through linkRoots wherever an
// exported symlink's absolute target names one of them. On error, any roots already opened
// are closed.
func New(exportsDir string, linkRoots ...string) (*Server, error) {
	exports, err := os.OpenRoot(exportsDir)
	if err != nil {
		return nil, err
	}
	s := &Server{exports: exports, allowed: []allowedRoot{{dir: path.Clean(exportsDir), root: exports}}}
	for _, dir := range linkRoots {
		root, err := os.OpenRoot(dir)
		if err != nil {
			for _, a := range s.allowed {
				_ = a.root.Close()
			}
			return nil, err
		}
		s.allowed = append(s.allowed, allowedRoot{dir: path.Clean(dir), root: root})
	}
	return s, nil
}

// Serve runs an sftp.RequestServer over conn until the client disconnects, ctx is done,
// or an unrecoverable error occurs.
func (s *Server) Serve(ctx context.Context, conn net.Conn) error {
	rs := sftp.NewRequestServer(conn, sftp.Handlers{
		FileGet:  s,
		FilePut:  s,
		FileCmd:  s,
		FileList: s,
	})
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = rs.Close()
		case <-done:
		}
	}()
	err := rs.Serve()
	close(done)
	return err
}

// rootRelative maps a client-supplied path p onto a path relative to the exports root: p is
// first cleaned as an absolute path, then agentconfig.ExportsMountPoint itself becomes ".",
// a path under it has that prefix stripped once, and anything else keeps its leading "/"
// stripped -- so a legacy absolute mount path like "/tel_app_exports/app/x" resolves to
// "app/x" under the root, "/" lists the root itself, and an unrelated absolute path such as
// "/etc/passwd" rebases to a path ("etc/passwd") that normally doesn't exist under the root
// rather than the real /etc/passwd.
func rootRelative(p string) string {
	clean := path.Clean("/" + p)
	var rel string
	switch {
	case clean == agentconfig.ExportsMountPoint:
		rel = "."
	case strings.HasPrefix(clean, agentconfig.ExportsMountPoint+"/"):
		rel = clean[len(agentconfig.ExportsMountPoint)+1:]
	default:
		rel = strings.TrimPrefix(clean, "/")
	}
	if rel == "" {
		rel = "."
	}
	return rel
}

// splitRel splits a clean, root-relative path (as rootRelative or a redirect target
// produces it) into its directory components and final component. The root itself (".")
// has no final component.
func splitRel(rel string) (dirs []string, leaf string, ok bool) {
	if rel == "." {
		return nil, "", false
	}
	parts := strings.Split(rel, "/")
	return parts[:len(parts)-1], parts[len(parts)-1], true
}

// readAbsoluteSymlink Lstats rel within root and, if it names a symlink whose literal
// target text is an absolute path, returns that text and true. Anything else -- rel doesn't
// exist, rel isn't a symlink, or rel is a symlink with a relative target -- reports
// ok=false with a nil error: a relative target is left for a native call on root to
// resolve, and a missing entry is left for the caller's real operation to report as such.
func (s *Server) readAbsoluteSymlink(root *os.Root, rel string) (target string, ok bool, err error) {
	info, err := root.Lstat(rel)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return "", false, nil
	}
	target, err = root.Readlink(rel)
	if err != nil {
		return "", false, err
	}
	if !path.IsAbs(target) {
		return "", false, nil
	}
	return target, true, nil
}

// matchAllowedRoot reports whether target -- the literal text of a symlink -- names one of
// s.allowed's directories, or a path under one of them, and if so returns that root and the
// part of target relative to it ("." if target names the root's directory itself).
func (s *Server) matchAllowedRoot(target string) (*os.Root, string, bool) {
	clean := path.Clean(target)
	for _, a := range s.allowed {
		if clean == a.dir {
			return a.root, ".", true
		}
		if rel, ok := strings.CutPrefix(clean, a.dir+"/"); ok {
			return a.root, rel, true
		}
	}
	return nil, "", false
}

// walkDirs checks each component of dirs, relative to start, for an absolute symlink.
// The first one found is redirected through matchAllowedRoot (os.ErrPermission if
// unclaimed) and the walk stops, returning that root and the redirect's target joined
// with the unwalked dirs. Relative symlinks and missing components stay in the prefix.
func (s *Server) walkDirs(start *os.Root, dirs []string) (root *os.Root, prefix string, err error) {
	for i, comp := range dirs {
		p := path.Join(prefix, comp)
		target, ok, rerr := s.readAbsoluteSymlink(start, p)
		if rerr != nil {
			return nil, "", rerr
		}
		if ok {
			newRoot, newRel, allowed := s.matchAllowedRoot(target)
			if !allowed {
				return nil, "", os.ErrPermission
			}
			rest := append([]string{newRel}, dirs[i+1:]...)
			return newRoot, path.Join(rest...), nil
		}
		prefix = p
	}
	return start, prefix, nil
}

// resolved is the result of resolving a client-facing path: rel names the resolved entry
// within root, one of the Server's long-lived, shared roots.
type resolved struct {
	root *os.Root
	rel  string
}

// resolve maps a client path to the root and root-relative name that serve it. Every
// directory component is always redirect-checked. followLeaf additionally controls the
// final component: true dereferences it exactly like the directory components (Stat, Open
// and friends must see through a symlink leaf); false leaves it alone, symlink or not, for
// operations that act on the entry itself (Remove, Mkdir, Lstat, Readlink).
func (s *Server) resolve(p string, followLeaf bool) (*resolved, error) {
	dirs, leaf, ok := splitRel(rootRelative(p))
	if !ok {
		return &resolved{root: s.exports, rel: "."}, nil
	}
	root, prefix, err := s.walkDirs(s.exports, dirs)
	if err != nil {
		return nil, err
	}
	rel := path.Join(prefix, leaf)
	if !followLeaf {
		return &resolved{root: root, rel: rel}, nil
	}
	for hops := 0; hops < maxRedirectHops; hops++ {
		target, ok, rerr := s.readAbsoluteSymlink(root, rel)
		if rerr != nil {
			return nil, rerr
		}
		if !ok {
			return &resolved{root: root, rel: rel}, nil
		}
		newRoot, newRel, allowed := s.matchAllowedRoot(target)
		if !allowed {
			return nil, os.ErrPermission
		}
		root, rel = newRoot, newRel
	}
	return nil, fmt.Errorf("sftpserver: too many nested symlinks resolving %q", p)
}

// withTwoPaths resolves from and to independently, exactly as a single-path operation
// would (following redirects, not the leaf). If both land under the same root it calls op
// once on that root with their root-relative paths, keeping the rename or link a single
// os.Root operation; if they land under different roots it returns syscall.EXDEV, the error
// a cross-device rename or link reports, without calling op.
func (s *Server) withTwoPaths(from, to string, op func(root *os.Root, fromRel, toRel string) error) error {
	fromRes, err := s.resolve(from, false)
	if err != nil {
		return err
	}

	toRes, err := s.resolve(to, false)
	if err != nil {
		return err
	}

	same, err := sameRoot(fromRes.root, toRes.root)
	if err != nil {
		return err
	}
	if !same {
		return syscall.EXDEV
	}
	return op(fromRes.root, fromRes.rel, toRes.rel)
}

// sameRoot reports whether a and b are open on the same directory. Identity is compared by
// device and inode, not by pointer, because resolving from and to independently can open two
// distinct *os.Root values on the same directory.
func sameRoot(a, b *os.Root) (bool, error) {
	if a == b {
		return true, nil
	}
	ai, err := a.Stat(".")
	if err != nil {
		return false, err
	}
	bi, err := b.Stat(".")
	if err != nil {
		return false, err
	}
	return os.SameFile(ai, bi), nil
}

// Fileread implements sftp.FileReader.
func (s *Server) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	res, err := s.resolve(r.Filepath, true)
	if err != nil {
		return nil, err
	}
	return res.root.OpenFile(res.rel, os.O_RDONLY, 0)
}

// Filewrite implements sftp.FileWriter.
func (s *Server) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	return s.OpenFile(r)
}

// OpenFile implements sftp.OpenFileWriter, needed because some clients (e.g. sshfs) open
// files O_RDWR and read and write through the same handle.
func (s *Server) OpenFile(r *sftp.Request) (sftp.WriterAtReaderAt, error) {
	res, err := s.resolve(r.Filepath, true)
	if err != nil {
		return nil, err
	}
	return res.root.OpenFile(res.rel, openFlags(r.Pflags()), 0o644)
}

// openFlags converts the SFTP open flags carried by a Request into the os.OpenFile flags
// os.Root.OpenFile expects.
func openFlags(f sftp.FileOpenFlags) int {
	flags := os.O_RDONLY
	switch {
	case f.Read && f.Write:
		flags = os.O_RDWR
	case f.Write:
		flags = os.O_WRONLY
	}
	if f.Append {
		flags |= os.O_APPEND
	}
	if f.Creat {
		flags |= os.O_CREATE
	}
	if f.Trunc {
		flags |= os.O_TRUNC
	}
	if f.Excl {
		flags |= os.O_EXCL
	}
	return flags
}

// Filecmd implements sftp.FileCmder.
func (s *Server) Filecmd(r *sftp.Request) error {
	switch r.Method {
	case "Setstat":
		res, err := s.resolve(r.Filepath, true)
		if err != nil {
			return err
		}
		return s.setstat(res.root, res.rel, r)

	case "Rename":
		return s.withTwoPaths(r.Filepath, r.Target, func(root *os.Root, from, to string) error {
			// SFTP-v2 rename fails if the target already exists; PosixRename is
			// the method that overwrites (see request-example.go's Filecmd).
			if _, err := root.Lstat(to); err == nil {
				return os.ErrExist
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return root.Rename(from, to)
		})

	case "Rmdir", "Remove":
		res, err := s.resolve(r.Filepath, false)
		if err != nil {
			return err
		}
		return res.root.Remove(res.rel)

	case "Mkdir":
		res, err := s.resolve(r.Filepath, false)
		if err != nil {
			return err
		}
		return res.root.Mkdir(res.rel, 0o755)

	case "Link":
		return s.withTwoPaths(r.Filepath, r.Target, func(root *os.Root, from, to string) error {
			return root.Link(from, to)
		})

	case "Symlink":
		// r.Filepath carries the symlink's target text and r.Target its link path
		// (see request-example.go's Filecmd); the target text is stored verbatim,
		// not resolved against any root.
		res, err := s.resolve(r.Target, false)
		if err != nil {
			return err
		}
		return res.root.Symlink(r.Filepath, res.rel)

	default:
		return fmt.Errorf("sftpserver: unsupported Filecmd method %q", r.Method)
	}
}

// setstat applies the attributes carried by a Setstat request to rel under root, in the same
// order request-example.go's Setstat implicitly assumes: size before mode, mode before
// times, times before ownership.
func (s *Server) setstat(root *os.Root, rel string, r *sftp.Request) error {
	flags := r.AttrFlags()
	attrs := r.Attributes()

	if flags.Size {
		f, err := root.OpenFile(rel, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		err = f.Truncate(int64(attrs.Size))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	if flags.Permissions {
		if err := root.Chmod(rel, attrs.FileMode()); err != nil {
			return err
		}
	}
	if flags.Acmodtime {
		if err := root.Chtimes(rel, attrs.AccessTime(), attrs.ModTime()); err != nil {
			return err
		}
	}
	if flags.UidGid {
		if err := root.Chown(rel, int(attrs.UID), int(attrs.GID)); err != nil {
			return err
		}
	}
	return nil
}

// PosixRename implements sftp.PosixRenameFileCmder: unlike Rename it overwrites an
// existing target, matching POSIX rename(2) semantics.
func (s *Server) PosixRename(r *sftp.Request) error {
	return s.withTwoPaths(r.Filepath, r.Target, func(root *os.Root, from, to string) error {
		return root.Rename(from, to)
	})
}

// listerat implements sftp.ListerAt over a fixed slice of os.FileInfo, the pattern used by
// github.com/pkg/sftp's own request-example.go.
type listerat []os.FileInfo

func (l listerat) ListAt(ls []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(ls, l[offset:])
	if n < len(ls) {
		return n, io.EOF
	}
	return n, nil
}

// symlinkInfo implements os.FileInfo for a Readlink response. Only Name is inspected by
// the caller (see filestat's "Readlink" case in github.com/pkg/sftp), and it must carry
// the raw (possibly relative, possibly absolute) link target text rather than a base name.
type symlinkInfo string

func (n symlinkInfo) Name() string       { return string(n) }
func (n symlinkInfo) Size() int64        { return 0 }
func (n symlinkInfo) Mode() os.FileMode  { return os.ModeSymlink }
func (n symlinkInfo) ModTime() time.Time { return time.Time{} }
func (n symlinkInfo) IsDir() bool        { return false }
func (n symlinkInfo) Sys() any           { return nil }

// Filelist implements sftp.FileLister for the List, Stat, and Readlink methods.
func (s *Server) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	switch r.Method {
	case "List":
		res, err := s.resolve(r.Filepath, true)
		if err != nil {
			return nil, err
		}
		f, err := res.root.Open(res.rel)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		entries, err := f.ReadDir(-1)
		if err != nil {
			return nil, err
		}
		infos := make([]os.FileInfo, 0, len(entries))
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				return nil, err
			}
			infos = append(infos, info)
		}
		return listerat(infos), nil

	case "Stat":
		res, err := s.resolve(r.Filepath, true)
		if err != nil {
			return nil, err
		}
		info, err := res.root.Stat(res.rel)
		if err != nil {
			return nil, err
		}
		return listerat{info}, nil

	case "Readlink":
		res, err := s.resolve(r.Filepath, false)
		if err != nil {
			return nil, err
		}
		target, err := res.root.Readlink(res.rel)
		if err != nil {
			return nil, err
		}
		return listerat{symlinkInfo(target)}, nil

	default:
		return nil, fmt.Errorf("sftpserver: unsupported Filelist method %q", r.Method)
	}
}

// Lstat implements sftp.LstatFileLister, so Lstat requests see symlinks themselves
// instead of what they point to.
func (s *Server) Lstat(r *sftp.Request) (sftp.ListerAt, error) {
	res, err := s.resolve(r.Filepath, false)
	if err != nil {
		return nil, err
	}
	info, err := res.root.Lstat(res.rel)
	if err != nil {
		return nil, err
	}
	return listerat{info}, nil
}
