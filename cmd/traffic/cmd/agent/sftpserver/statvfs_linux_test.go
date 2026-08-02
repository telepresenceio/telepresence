//go:build linux

package sftpserver_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStatVFS(t *testing.T) {
	client, _ := newTestServer(t)

	vfs, err := client.StatVFS("/tel_app_exports/app")
	require.NoError(t, err)
	require.NotZero(t, vfs.Blocks)
}

func TestMountsLinkStatVFS(t *testing.T) {
	client, tree := newMountsTestServer(t)

	vfs, err := client.StatVFS("/tel_app_exports/" + tree.container + "/etc")
	require.NoError(t, err)
	require.NotZero(t, vfs.Blocks)
}
