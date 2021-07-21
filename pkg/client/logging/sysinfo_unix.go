// +build !windows

package logging

import (
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
