// +build linux darwin

package logging

import (
	"os"
	"syscall"
)

type unixSysInfo syscall.Stat_t

func getSysInfo(info os.FileInfo) sysinfo {
	return (*unixSysInfo)(info.Sys().(*syscall.Stat_t))
}

func (u *unixSysInfo) setOwnerAndGroup(name string) error {
	return os.Chown(name, int(u.Uid), int(u.Gid))
}

func (u *unixSysInfo) haveSameOwnerAndGroup(other sysinfo) bool {
	ou := other.(*unixSysInfo)
	return u.Uid == ou.Uid && u.Gid == ou.Gid
}
