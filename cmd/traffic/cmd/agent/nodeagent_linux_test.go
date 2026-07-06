//go:build linux

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/procfs"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// Test_appEnvironment_transform guards the AppEnvironment split: feeding the same
// transformation a synthetic slice (rather than the process's own dos.Environ) must
// still apply prefix handling, skip-keys, and the EnvInterceptContainer/mount-derived
// vars identically.
func Test_appEnvironment_transform(t *testing.T) {
	cn := &agentconfig.Container{
		Name:      "app",
		EnvPrefix: "A_",
		Mounts: types.MountPolicies{
			"/home/bob": types.MountPolicyRemote,
			"/tmp":      types.MountPolicyLocal,
		},
	}
	env := appEnvironment([]string{
		"HOME=/home/tel",
		"PATH=/bin:/usr/bin",
		"HOSTNAME=somehost",
		"ZULU=zulu",
		agentconfig.EnvPrefixApp + "A_ALPHA=alpha",
		agentconfig.EnvPrefixApp + "B_BRAVO=bravo",
	}, cn)

	require.Equal(t, map[string]string{
		"ZULU":                            "zulu",
		"ALPHA":                           "alpha",
		agentconfig.EnvInterceptContainer: "app",
		agentconfig.EnvInterceptMounts:    "/home/bob",
		agentconfig.EnvLocalMounts:        "/tmp",
	}, env)
}

// Test_exportProcMounts creates a symlink tree under a temp exportsRoot pointing into
// the current process's own procfs root, then verifies the symlink resolves to the
// expected procfs.RootPath target and reads through correctly.
func Test_exportProcMounts(t *testing.T) {
	exportsRoot := t.TempDir()
	pid := os.Getpid()

	srcDir := t.TempDir()
	markerFile := filepath.Join(srcDir, "marker")
	require.NoError(t, os.WriteFile(markerFile, []byte("hello"), 0o600))

	cn := &agentconfig.Container{
		Name:       "app",
		MountPoint: "/tel_app_mounts/app",
		Mounts: types.MountPolicies{
			markerFile: types.MountPolicyRemote,
		},
	}

	require.NoError(t, exportProcMounts(context.Background(), exportsRoot, pid, cn))

	link := filepath.Join(exportsRoot, "app", markerFile)
	target, err := os.Readlink(link)
	require.NoError(t, err)
	require.Equal(t, procfs.RootPath(pid, markerFile), target)

	data, err := os.ReadFile(link)
	require.NoError(t, err)
	require.Equal(t, "hello", string(data))
}

// Test_exportProcMounts_restart verifies that a pre-existing export subdir (simulating
// a restarted node-agent container) is replaced rather than left with stale symlinks.
func Test_exportProcMounts_restart(t *testing.T) {
	exportsRoot := t.TempDir()
	pid := os.Getpid()

	srcDir := t.TempDir()
	markerFile := filepath.Join(srcDir, "marker")
	require.NoError(t, os.WriteFile(markerFile, []byte("hello"), 0o600))

	cn := &agentconfig.Container{
		Name:       "app",
		MountPoint: "/tel_app_mounts/app",
		Mounts: types.MountPolicies{
			markerFile: types.MountPolicyRemote,
		},
	}

	require.NoError(t, exportProcMounts(context.Background(), exportsRoot, pid, cn))
	require.NoError(t, exportProcMounts(context.Background(), exportsRoot, pid, cn))

	link := filepath.Join(exportsRoot, "app", markerFile)
	data, err := os.ReadFile(link)
	require.NoError(t, err)
	require.Equal(t, "hello", string(data))
}

func Test_parseContainerIDs(t *testing.T) {
	_, err := parseContainerIDs("")
	require.Error(t, err)

	_, err = parseContainerIDs("not json")
	require.Error(t, err)

	ids, err := parseContainerIDs(`{"app":"containerd://abc123"}`)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"app": "containerd://abc123"}, ids)
}
