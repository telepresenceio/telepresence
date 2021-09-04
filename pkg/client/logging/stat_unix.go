//go:build !windows
// +build !windows

package logging

import (
	"fmt"
	"os"
	"syscall"
)

type unixSysInfo syscall.Stat_t

func GetSysInfo(_ string, info os.FileInfo) (SysInfo, error) {
	return (*unixSysInfo)(info.Sys().(*syscall.Stat_t)), nil
}

func (u *unixSysInfo) SetOwnerAndGroup(name string) error {
	return os.Chown(name, int(u.Uid), int(u.Gid))
}

func (u *unixSysInfo) HaveSameOwnerAndGroup(other SysInfo) bool {
	ou := other.(*unixSysInfo)
	return u.Uid == ou.Uid && u.Gid == ou.Gid
}

func (u *unixSysInfo) String() string {
	return fmt.Sprintf("CTIME %v, UID %d, GID %d", u.BirthTime(), u.Uid, u.Gid)
}
