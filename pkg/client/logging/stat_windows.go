package logging

import (
	"errors"
	"fmt"
	"runtime"
	"syscall" //nolint:depguard // We specifically need "syscall.Win32FileAttributeData" rather than "windows.Win32FileAttributeData" for fs.File.Sys().
	"time"

	"github.com/hectane/go-acl/api"
	"golang.org/x/sys/windows"

	"github.com/telepresenceio/telepresence/v2/pkg/dos"
)

type WindowsSysInfo interface {
	SysInfo
	Owner() *windows.SID
	Group() *windows.SID
	DACL() windows.Handle
	SACL() windows.Handle
	SecurityDescriptor() windows.Handle
}

type windowsSysInfo struct {
	path    string
	data    *syscall.Win32FileAttributeData
	owner   *windows.SID
	group   *windows.SID
	dacl    windows.Handle
	sacl    windows.Handle
	secDesc windows.Handle
}

func osFStat(file dos.File) (SysInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat %s: %w", file.Name(), err)
	}
	sys, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return nil, fmt.Errorf("files of type %T don't support Fstat", file)
	}
	wi := windowsSysInfo{
		path: file.Name(),
		data: sys,
	}
	err = api.GetNamedSecurityInfo(
		wi.path,
		api.SE_FILE_OBJECT,
		api.OWNER_SECURITY_INFORMATION,
		&wi.owner,
		&wi.group,
		&wi.dacl,
		&wi.sacl,
		&wi.secDesc,
	)
	if err != nil && !errors.Is(err, windows.ERROR_SUCCESS) {
		return nil, err
	}
	runtime.SetFinalizer(&wi, func(wi *windowsSysInfo) {
		_, _ = windows.LocalFree(wi.secDesc)
	})
	return &wi, nil
}

func (wi *windowsSysInfo) Size() int64 {
	return int64(wi.data.FileSizeHigh)<<32 + int64(wi.data.FileSizeLow)
}

func (wi *windowsSysInfo) SetOwnerAndGroup(name string) error {
	err := api.SetNamedSecurityInfo(name, api.SE_FILE_OBJECT, api.OWNER_SECURITY_INFORMATION, wi.owner, wi.group, wi.dacl, wi.sacl)
	if err != nil {
		// On some systems it seems SetNamedSecurityInfo will return ERROR_SUCCESS on success... this is an odd violation of the principle
		// that windows APIs return err = nil on success but okay
		if errors.Is(err, windows.ERROR_SUCCESS) {
			return nil
		}
		return err
	}
	return nil
}

func (wi *windowsSysInfo) HaveSameOwnerAndGroup(s SysInfo) bool {
	eq := func(a, b *windows.SID) bool {
		if a == b {
			return true
		}
		if a == nil || b == nil {
			return false
		}
		if a.IsValid() {
			if b.IsValid() {
				return a.Equals(b)
			}
			return false
		}
		return !b.IsValid()
	}
	owi, ok := s.(*windowsSysInfo)
	return ok && eq(wi.owner, owi.owner) && eq(wi.group, owi.group)
}

func (wi *windowsSysInfo) BirthTime() time.Time {
	return time.Unix(0, wi.data.CreationTime.Nanoseconds())
}

func (wi *windowsSysInfo) ModifyTime() time.Time {
	return time.Unix(0, wi.data.LastWriteTime.Nanoseconds())
}

func (wi *windowsSysInfo) ChangeTime() time.Time {
	return time.Unix(0, wi.data.LastWriteTime.Nanoseconds())
}

func (wi *windowsSysInfo) Owner() *windows.SID {
	return wi.owner
}

func (wi *windowsSysInfo) Group() *windows.SID {
	return wi.group
}

func (wi *windowsSysInfo) DACL() windows.Handle {
	return wi.dacl
}

func (wi *windowsSysInfo) SACL() windows.Handle {
	return wi.sacl
}

func (wi *windowsSysInfo) String() string {
	ov := "invalid"
	if wi.owner != nil && wi.owner.IsValid() {
		ov = wi.owner.String()
	}
	gv := "invalid"
	if wi.group != nil && wi.group.IsValid() {
		gv = wi.group.String()
	}
	return fmt.Sprintf("CTIME %v, UID %v, GID %v", wi.BirthTime(), ov, gv)
}
