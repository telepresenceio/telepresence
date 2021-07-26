package logging

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hectane/go-acl/api"
	"golang.org/x/sys/windows"
)

func dupToStd(_ *os.File) interface{} {
	return errors.New("dupToStd() is not implemented on windows")
}

type WindowsSysInfo interface {
	SysInfo
	Owner() *windows.SID
	Group() *windows.SID
	DACL() windows.Handle
	SACL() windows.Handle
	SecurityDescriptor() windows.Handle
}

type windowsSysInfo struct {
	path  string
	data  *syscall.Win32FileAttributeData
	owner *windows.SID
	group *windows.SID
	dacl  windows.Handle
	sacl  windows.Handle
}

func GetSysInfo(dir string, info os.FileInfo) (SysInfo, error) {
	wi := windowsSysInfo{
		path: filepath.Join(dir, info.Name()),
		data: info.Sys().(*syscall.Win32FileAttributeData),
	}
	var secDesc windows.Handle
	err := api.GetNamedSecurityInfo(
		wi.path,
		api.SE_FILE_OBJECT,
		api.OWNER_SECURITY_INFORMATION,
		&wi.owner,
		&wi.group,
		&wi.dacl,
		&wi.sacl,
		&secDesc,
	)
	if err != nil && !errors.Is(err, windows.ERROR_SUCCESS) {
		return nil, err
	}
	if _, err = windows.LocalFree(secDesc); err != nil && !errors.Is(err, windows.ERROR_SUCCESS) {
		return nil, err
	}
	return &wi, nil
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
		return a.Equals(b)
	}
	owi, ok := s.(*windowsSysInfo)
	return ok && eq(wi.owner, owi.owner) && eq(wi.group, owi.group)
}

func (wi *windowsSysInfo) BirthTime() time.Time {
	return time.Unix(0, wi.data.CreationTime.Nanoseconds())
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
	return fmt.Sprintf("CTIME %v, UID %v, GID %v", wi.BirthTime(), wi.owner, wi.group)
}
