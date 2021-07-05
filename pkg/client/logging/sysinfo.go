package logging

import "time"

// SysInfo represents the elaborate info in a FileInfo.Sys(). The implementations are
// os specific.
//
// Unix:
//  info.Sys().(*syscall.Stat_t)
//
// Windows:
//  info.Sys().(*syscall.Win32FileAttributeData)
type SysInfo interface {
	Birthtime() time.Time

	SetOwnerAndGroup(name string) error

	HaveSameOwnerAndGroup(SysInfo) bool
}
