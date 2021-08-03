package logging

import (
	"fmt"
	"time"
)

// SysInfo represents the elaborate info in a FileInfo.Sys(). The implementations are
// os specific.
//
// Unix:
//  info.Sys().(*syscall.Stat_t)
//
// Windows:
//  info.Sys().(*syscall.Win32FileAttributeData)
type SysInfo interface {
	fmt.Stringer

	BirthTime() time.Time

	SetOwnerAndGroup(name string) error

	HaveSameOwnerAndGroup(SysInfo) bool
}
