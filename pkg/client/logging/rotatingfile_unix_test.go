//go:build !windows

package logging_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
)

func TestRotatingFile_RefusesSymlinkedLogFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	require.NoError(t, os.WriteFile(target, []byte("keep"), 0o600))
	logPath := filepath.Join(dir, "x.log")
	require.NoError(t, os.Symlink(target, logPath))

	_, err := logging.OpenRotatingFile(t.Context(), logPath, "20060102T150405", false, 0o600, logging.RotateNever, 5)
	require.Error(t, err)
	b, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "keep", string(b))
}

func TestRotatingFile_RefusesDanglingSymlinkedLogFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	logPath := filepath.Join(dir, "x.log")
	require.NoError(t, os.Symlink(target, logPath))

	_, err := logging.OpenRotatingFile(t.Context(), logPath, "20060102T150405", false, 0o600, logging.RotateNever, 5)
	require.Error(t, err)
	assert.NoFileExists(t, target)
}

func TestRotatingFile_RotationDoesNotFollowTempLink(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "x.log")
	rf, err := logging.OpenRotatingFile(t.Context(), logPath, "20060102T150405", false, 0o600, logging.RotateNever, 5)
	require.NoError(t, err)
	defer rf.Close()
	_, err = rf.Write([]byte("first\n"))
	require.NoError(t, err)

	existing := filepath.Join(dir, "existing")
	require.NoError(t, os.WriteFile(existing, []byte("keep"), 0o600))
	require.NoError(t, os.Symlink(existing, logPath+".tmp"))

	require.NoError(t, rf.Rotate())
	b, err := os.ReadFile(existing)
	require.NoError(t, err)
	assert.Equal(t, "keep", string(b))

	fi, err := os.Lstat(logPath)
	require.NoError(t, err)
	assert.True(t, fi.Mode().IsRegular())
	assert.NoFileExists(t, logPath+".tmp")
}

func TestRotatingFile_RotationDoesNotCreateDanglingTempLinkTarget(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "x.log")
	rf, err := logging.OpenRotatingFile(t.Context(), logPath, "20060102T150405", false, 0o600, logging.RotateNever, 5)
	require.NoError(t, err)
	defer rf.Close()
	_, err = rf.Write([]byte("first\n"))
	require.NoError(t, err)

	missing := filepath.Join(dir, "missing")
	require.NoError(t, os.Symlink(missing, logPath+".tmp"))
	require.NoError(t, rf.Rotate())
	assert.NoFileExists(t, missing)
}

func TestRotatingFile_RotationWorks(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "x.log")
	rf, err := logging.OpenRotatingFile(t.Context(), logPath, "20060102T150405", false, 0o600, logging.RotateNever, 5)
	require.NoError(t, err)
	defer rf.Close()
	_, err = rf.Write([]byte("first\n"))
	require.NoError(t, err)
	require.NoError(t, rf.Rotate())
	_, err = rf.Write([]byte("second\n"))
	require.NoError(t, err)

	b, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Equal(t, "second\n", string(b))
	files, err := filepath.Glob(filepath.Join(dir, "x-*.log"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	b, err = os.ReadFile(files[0])
	require.NoError(t, err)
	assert.Equal(t, "first\n", string(b))
}
