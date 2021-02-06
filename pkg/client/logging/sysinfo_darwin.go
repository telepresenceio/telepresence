package logging

import (
	"os"
	"syscall"
	"time"
)

type unixSysInfo syscall.Stat_t

func getSysInfo(info os.FileInfo) sysinfo {
	return (*unixSysInfo)(info.Sys().(*syscall.Stat_t))
}

func (u *unixSysInfo) birthtime() time.Time {
	sec, nsec := u.Birthtimespec.Unix()
	return time.Unix(sec, nsec)
}

func (u *unixSysInfo) ensureOwnerAndGroup(name string) error {
	info, err := os.Stat(name)
	if err != nil {
		return err
	}
	ou := info.Sys().(*syscall.Stat_t)
	if u.Uid != ou.Uid || u.Gid != ou.Gid {
		err = os.Chown(name, int(u.Uid), int(u.Gid))
	}
	return err
}
