package filelocation

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileDeleteChild is FILE_DELETE_CHILD, which x/sys/windows does not define.
const fileDeleteChild = 0x40

// Access masks that let a user other than an administrator change an element or who may change it.
const (
	fileChangeMask = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER |
		windows.GENERIC_WRITE | windows.GENERIC_ALL
	// For a directory, FILE_ADD_FILE and FILE_ADD_SUBDIRECTORY are left out. Windows grants them to users below
	// C:\ProgramData, and whatever a user adds is owned by that user, which fails the owner check.
	dirChangeMask = fileDeleteChild | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.GENERIC_WRITE | windows.GENERIC_ALL
)

// ACE types that allow access but carry conditions or object scoping that are not evaluated here.
const (
	accessAllowedCompoundACEType       = 4
	accessAllowedObjectACEType         = 5
	accessAllowedCallbackACEType       = 9
	accessAllowedCallbackObjectACEType = 11
)

const trustedInstallerSid = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

// administratorSids returns LocalSystem, BUILTIN\Administrators and TrustedInstaller.
//
//nolint:gochecknoglobals // computed once
var administratorSids = sync.OnceValues(func() ([]*windows.SID, error) {
	sids := make([]*windows.SID, 0, 3)
	for _, wk := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		sid, err := windows.CreateWellKnownSid(wk)
		if err != nil {
			return nil, err
		}
		sids = append(sids, sid)
	}
	sid, err := windows.StringToSid(trustedInstallerSid)
	if err != nil {
		return nil, err
	}
	return append(sids, sid), nil
})

func isAdministrator(sid *windows.SID, admins []*windows.SID) bool {
	for _, a := range admins {
		if sid.Equals(a) {
			return true
		}
	}
	return false
}

func checkManaged(path, root string) error {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	sep := string(filepath.Separator)
	if !strings.EqualFold(path, root) && !(len(path) > len(root) && strings.EqualFold(path[:len(root)], root) && strings.HasPrefix(path[len(root):], sep)) {
		return &UntrustedError{Path: path, Reason: fmt.Sprintf("it is not located under %s", root)}
	}
	admins, err := administratorSids()
	if err != nil {
		return &UntrustedError{Path: path, Reason: err.Error()}
	}
	for p := path; ; p = filepath.Dir(p) {
		if reason, err := checkManagedElement(p, admins); err != nil {
			return err
		} else if reason != "" {
			return &UntrustedError{Path: path, Reason: reason}
		}
		if strings.EqualFold(p, root) {
			return nil
		}
	}
}

// checkManagedElement returns the reason p cannot be trusted, or "" when it can. It must not be a reparse point,
// it must be owned by an administrator, and its DACL must not allow anyone else to change it.
func checkManagedElement(p string, admins []*windows.SID) (string, error) {
	pp, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return "", err
	}
	attrs, err := windows.GetFileAttributes(pp)
	if err != nil {
		return "", err
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Sprintf("%s is a symbolic link or junction", p), nil
	}
	sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return fmt.Sprintf("the owner of %s cannot be determined", p), nil
	}
	if !isAdministrator(owner, admins) {
		return fmt.Sprintf("%s is owned by %s, not by an administrator", p, owner), nil
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Sprintf("%s has no access control list", p), nil
	}
	mask := uint32(fileChangeMask)
	if attrs&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		mask = dirChangeMask
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(dacl, i, &ace); err != nil {
			return "", err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE:
		case accessAllowedCompoundACEType, accessAllowedObjectACEType, accessAllowedCallbackACEType, accessAllowedCallbackObjectACEType:
			return fmt.Sprintf("%s has an access control entry of type %d that cannot be evaluated", p, ace.Header.AceType), nil
		default:
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if isAdministrator(sid, admins) {
			continue
		}
		if uint32(ace.Mask)&mask != 0 {
			return fmt.Sprintf("%s has an access control entry that allows %s to make changes (access mask 0x%x)", p, sid, uint32(ace.Mask)), nil
		}
	}
	return "", nil
}
