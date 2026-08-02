//go:build linux

package sftpserver

import (
	"github.com/pkg/sftp"
	"golang.org/x/sys/unix"
)

// StatVFS implements sftp.StatVFSFileCmder, the handler behind the statvfs@openssh.com
// extension. It mirrors the conversion github.com/pkg/sftp's own server_statvfs_linux.go
// applies to a syscall.Statfs_t, but statfs's the file opened at r.Filepath under whichever
// root resolve picks instead of the raw path a plain sftp.Server would use.
func (s *Server) StatVFS(r *sftp.Request) (*sftp.StatVFS, error) {
	root, rel, err := s.resolve(r.Filepath)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var stat unix.Statfs_t
	if err := unix.Fstatfs(int(f.Fd()), &stat); err != nil {
		return nil, err
	}
	return &sftp.StatVFS{
		Bsize:   uint64(stat.Bsize),
		Frsize:  uint64(stat.Frsize),
		Blocks:  stat.Blocks,
		Bfree:   stat.Bfree,
		Bavail:  stat.Bavail,
		Files:   stat.Files,
		Ffree:   stat.Ffree,
		Favail:  stat.Ffree,
		Flag:    uint64(stat.Flags),
		Namemax: uint64(stat.Namelen),
	}, nil
}
