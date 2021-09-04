//go:build !windows
// +build !windows

package logging

import (
	"fmt"
	"os"
	"syscall"
)

type fileInfo struct {
	*syscall.Stat_t
}

func GetSysInfo(_ string, info os.FileInfo) (SysInfo, error) {
	return fileInfo{info.Sys().(*syscall.Stat_t)}, nil
}

func (u fileInfo) SetOwnerAndGroup(name string) error {
	return os.Chown(name, int(u.Uid), int(u.Gid))
}

func (u fileInfo) HaveSameOwnerAndGroup(other SysInfo) bool {
	ou := other.(fileInfo)
	return u.Uid == ou.Uid && u.Gid == ou.Gid
}

func (u fileInfo) String() string {
	return fmt.Sprintf("CTIME %v, UID %d, GID %d", u.BirthTime(), u.Uid, u.Gid)
}
