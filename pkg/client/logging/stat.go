package logging

import (
	"fmt"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/dos"
)

// FStat returns the file status/info of an open file.
func FStat(file dos.File) (SysInfo, error) {
	return osFStat(file)
}

// SysInfo represents the elaborate info in a FileInfo.Sys(). The implementations are
// os specific.
//
// Unix:
//
//	info.Sys().(*syscall.Stat_t)
//
// Windows:
//
//	info.Sys().(*syscall.Win32FileAttributeData)
type SysInfo interface {
	fmt.Stringer

	Size() int64

	BirthTime() time.Time
	ModifyTime() time.Time // most recent content change
	ChangeTime() time.Time // most recent metadata change

	SetOwnerAndGroup(name string) error

	HaveSameOwnerAndGroup(SysInfo) bool
}
