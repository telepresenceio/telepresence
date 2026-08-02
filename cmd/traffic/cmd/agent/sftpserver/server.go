// Package sftpserver implements an SFTP server confined to two directory trees: the exports
// tree (the traffic-agent's own view of what it serves) and the mounts tree that the
// exported tree's top-level entries symlink into. It is built on sftp.NewRequestServer with
// Handlers backed by two *os.Root, one per tree, so every request -- however it names its
// path -- is resolved under one of them and can never escape either, even through ".."
// segments or a symlink that points elsewhere.
//
// Clients request paths under agentconfig.ExportsMountPoint (the traffic-agent's own view
// of the tree) as well as bare relative paths and "/"; rootRelative maps all of them onto
// the exports root before any os.Root call is made. cmd/traffic/cmd/agent/config.go's
// addAppMounts populates each container's exports directory with one absolute symlink per
// exported top-level mount, pointing into the mounts tree; os.Root refuses to traverse an
// absolute symlink under any circumstance, so resolve reads such a link itself and switches
// to the mounts root to serve whatever is past it.
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
	"time"

	"github.com/pkg/sftp"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
)

// Server serves SFTP connections confined to the exports and mounts directory trees passed
// to New. All connections a single Server hands to Serve share its os.Root values and can
// therefore be served concurrently.
type Server struct {
	exports   *os.Root
	mounts    *os.Root
	mountsDir string
}

// New opens exportsDir and mountsDir as os.Root values and returns a Server that confines
// every request to one or the other. exportsDir is the tree clients see directly; mountsDir
// is where an absolute symlink under exportsDir may point (agentconfig.MountPrefixApp in
// production).
func New(exportsDir, mountsDir string) (*Server, error) {
	exports, err := os.OpenRoot(exportsDir)
	if err != nil {
		return nil, err
	}
	mounts, err := os.OpenRoot(mountsDir)
	if err != nil {
		_ = exports.Close()
		return nil, err
	}
	return &Server{exports: exports, mounts: mounts, mountsDir: path.Clean(mountsDir)}, nil
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

// resolve maps a client path to the os.Root that should serve it and the path relative to
// that root that names it there. A path of one component or less ("." or a bare container
// directory) is always exports-relative. Otherwise the path splits into <container>/<top>/
// <rest...>; <container>/<top> is Lstat'd in the exports root. If that Lstat fails because
// the entry doesn't exist yet (e.g. a file about to be created there) or finds an entry that
// isn't a symlink, rel stays exports-relative -- the layout addAppMounts never touches, and
// the one the sftpserver unit tests use. If it is a symlink, its target must be underMounts
// -- the agent only ever writes links pointing into mountsDir -- and rest, if any, is
// resolved underneath it in the mounts root.
func (s *Server) resolve(p string) (*os.Root, string, error) {
	rel := rootRelative(p)
	parts := strings.SplitN(rel, "/", 3)
	if len(parts) < 2 {
		return s.exports, rel, nil
	}
	linkRel := parts[0] + "/" + parts[1]
	info, err := s.exports.Lstat(linkRel)
	switch {
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return nil, "", err
	case err != nil || info.Mode()&os.ModeSymlink == 0:
		return s.exports, rel, nil
	}
	target, err := s.exports.Readlink(linkRel)
	if err != nil {
		return nil, "", err
	}
	mountsRel, ok := s.underMounts(target)
	if !ok {
		return nil, "", os.ErrPermission
	}
	if len(parts) == 3 {
		mountsRel = path.Join(mountsRel, parts[2])
	}
	return s.mounts, mountsRel, nil
}

// resolveLink is resolve's counterpart for the Lstat and Readlink methods: a path of two
// components or less names an entry directly in the exports root (the container directory
// or one of its immediate children), and those methods must see such an entry as itself --
// symlink or not -- rather than follow it. Anything deeper only exists past such a symlink,
// so it falls back to resolve, which crosses into the mounts root to reach it.
func (s *Server) resolveLink(p string) (*os.Root, string, error) {
	rel := rootRelative(p)
	if len(strings.SplitN(rel, "/", 3)) < 3 {
		return s.exports, rel, nil
	}
	return s.resolve(p)
}

// underMounts reports whether target -- the text of a symlink found directly under the
// exports root -- names s.mountsDir or a path under it, and if so returns the part relative
// to s.mountsDir. Any other target is refused: addAppMounts never writes a link pointing
// anywhere else, so one that does is either stale or hostile.
func (s *Server) underMounts(target string) (string, bool) {
	target = path.Clean(target)
	if target == s.mountsDir {
		return ".", true
	}
	if rel, ok := strings.CutPrefix(target, s.mountsDir+"/"); ok {
		return rel, true
	}
	return "", false
}

// Fileread implements sftp.FileReader.
func (s *Server) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	root, rel, err := s.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}
	return root.OpenFile(rel, os.O_RDONLY, 0)
}

// Filewrite implements sftp.FileWriter.
func (s *Server) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	return s.OpenFile(r)
}

// OpenFile implements sftp.OpenFileWriter, needed because some clients (e.g. sshfs) open
// files O_RDWR and read and write through the same handle.
func (s *Server) OpenFile(r *sftp.Request) (sftp.WriterAtReaderAt, error) {
	root, rel, err := s.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}
	return root.OpenFile(rel, openFlags(r.Pflags()), 0o644)
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
		root, rel, err := s.resolve(r.Filepath)
		if err != nil {
			return err
		}
		return s.setstat(root, rel, r)

	case "Rename":
		root, rel, target, err := s.resolveTwo(r.Filepath, r.Target)
		if err != nil {
			return err
		}
		// SFTP-v2 rename fails if the target already exists; PosixRename is the
		// method that overwrites (see request-example.go's Filecmd).
		if _, err := root.Lstat(target); err == nil {
			return os.ErrExist
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return root.Rename(rel, target)

	case "Rmdir", "Remove":
		root, rel, err := s.resolve(r.Filepath)
		if err != nil {
			return err
		}
		return root.Remove(rel)

	case "Mkdir":
		root, rel, err := s.resolve(r.Filepath)
		if err != nil {
			return err
		}
		return root.Mkdir(rel, 0o755)

	case "Link":
		root, rel, target, err := s.resolveTwo(r.Filepath, r.Target)
		if err != nil {
			return err
		}
		return root.Link(rel, target)

	case "Symlink":
		// r.Filepath carries the symlink's target text and r.Target its link path
		// (see request-example.go's Filecmd); the target text is stored verbatim,
		// not resolved against either root.
		root, rel, err := s.resolve(r.Target)
		if err != nil {
			return err
		}
		return root.Symlink(r.Filepath, rel)

	default:
		return fmt.Errorf("sftpserver: unsupported Filecmd method %q", r.Method)
	}
}

// resolveTwo resolves the two paths of a Rename or Link request and requires them to land in
// the same root: a rename or hard link across the exports/mounts boundary would fail with
// EXDEV on a real filesystem too.
func (s *Server) resolveTwo(from, to string) (root *os.Root, fromRel, toRel string, err error) {
	root, fromRel, err = s.resolve(from)
	if err != nil {
		return nil, "", "", err
	}
	toRoot, toRel, err := s.resolve(to)
	if err != nil {
		return nil, "", "", err
	}
	if root != toRoot {
		return nil, "", "", fmt.Errorf("sftpserver: %q and %q are not on the same device", from, to)
	}
	return root, fromRel, toRel, nil
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
	root, rel, target, err := s.resolveTwo(r.Filepath, r.Target)
	if err != nil {
		return err
	}
	return root.Rename(rel, target)
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
		root, rel, err := s.resolve(r.Filepath)
		if err != nil {
			return nil, err
		}
		f, err := root.Open(rel)
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
		root, rel, err := s.resolve(r.Filepath)
		if err != nil {
			return nil, err
		}
		info, err := root.Stat(rel)
		if err != nil {
			return nil, err
		}
		return listerat{info}, nil

	case "Readlink":
		root, rel, err := s.resolveLink(r.Filepath)
		if err != nil {
			return nil, err
		}
		target, err := root.Readlink(rel)
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
	root, rel, err := s.resolveLink(r.Filepath)
	if err != nil {
		return nil, err
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	return listerat{info}, nil
}
