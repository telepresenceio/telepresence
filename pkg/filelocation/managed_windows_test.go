package filelocation

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// applySDDL replaces the owner and the DACL of path with those of sddl, without inherited entries.
func applySDDL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	require.NoError(t, err)
	owner, _, err := sd.Owner()
	require.NoError(t, err)
	dacl, _, err := sd.DACL()
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, dacl, nil))
}

// This is a unit test because the regression suite cannot change owners and access lists in the machine-wide
// directory of the machine it runs on.
func TestCheckManaged(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("giving a file the owner BUILTIN\\Administrators requires an elevated token")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	self := "O:" + user.User.Sid.String()

	const (
		admins   = "(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
		dirOwner = "O:BAD:P"
		dir      = dirOwner + admins + "(A;OICI;GR;;;BU)"
		file     = dirOwner + "(A;;FA;;;SY)(A;;FA;;;BA)(A;;GR;;;BU)"
	)
	tests := []struct {
		name   string
		root   string
		dir    string
		file   string
		reason string
	}{
		{name: "protected directory", root: dir, dir: dir, file: file},
		{name: "users may add files", root: dir, dir: dirOwner + admins + "(A;CI;0x1200af;;;BU)", file: file},
		{name: "file owned by the user", root: dir, dir: dir, file: self + "D:P(A;;FA;;;SY)(A;;FA;;;BA)", reason: "is owned by"},
		{name: "users may write the file", root: dir, dir: dir, file: dirOwner + "(A;;FA;;;SY)(A;;FA;;;BA)(A;;0x2;;;BU)", reason: "allows"},
		{name: "users may delete children", root: dir, dir: dirOwner + admins + "(A;CI;0x40;;;BU)", file: file, reason: "allows"},
		{name: "directory owned by the user", root: dir, dir: self + "D:P" + admins, file: file, reason: "is owned by"},
		{name: "root owned by the user", root: self + "D:P" + admins, dir: dir, file: file, reason: "is owned by"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "managed")
			sub := filepath.Join(root, "rootd")
			path := filepath.Join(sub, "daemon.json")
			require.NoError(t, os.MkdirAll(sub, 0o755))
			require.NoError(t, os.WriteFile(path, []byte("{}"), 0o644))
			applySDDL(t, path, tt.file)
			applySDDL(t, sub, tt.dir)
			applySDDL(t, root, tt.root)

			err := CheckManaged(path, root)
			if tt.reason == "" {
				assert.NoError(t, err)
				return
			}
			var ue *UntrustedError
			require.ErrorAs(t, err, &ue)
			assert.Equal(t, path, ue.Path)
			assert.Contains(t, ue.Reason, tt.reason)
		})
	}

	t.Run("outside the root", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "managed")
		var ue *UntrustedError
		require.ErrorAs(t, CheckManaged(filepath.Join(filepath.Dir(root), "x"), root), &ue)
		assert.Contains(t, ue.Reason, "not located under")
	})
	t.Run("missing file", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "managed")
		require.NoError(t, os.MkdirAll(root, 0o755))
		applySDDL(t, root, dir)
		err := CheckManaged(filepath.Join(root, "daemon.json"), root)
		require.Error(t, err)
		assert.True(t, errors.Is(err, fs.ErrNotExist))
		var ue *UntrustedError
		assert.False(t, errors.As(err, &ue))
	})
}
