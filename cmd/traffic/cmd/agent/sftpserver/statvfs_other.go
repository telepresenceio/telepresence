//go:build !linux

package sftpserver

import "github.com/pkg/sftp"

// StatVFS implements sftp.StatVFSFileCmder. The statvfs@openssh.com extension is only
// implemented for Linux, mirroring github.com/pkg/sftp's own non-Linux stub.
func (s *Server) StatVFS(_ *sftp.Request) (*sftp.StatVFS, error) {
	return nil, sftp.ErrSSHFxOpUnsupported
}
