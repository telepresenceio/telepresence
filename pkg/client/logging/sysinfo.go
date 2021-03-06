package logging

import "time"

// sysinfo represents the elaborate info in a FileInfo.Sys(). The implementations are
// os specific.
//
// Unix:
//  info.Sys().(*syscall.Stat_t)
//
// Windows:
//  info.Sys().(*syscall.Win32FileAttributeData)
type sysinfo interface {
	birthtime() time.Time

	setOwnerAndGroup(name string) error

	haveSameOwnerAndGroup(sysinfo) bool
}
