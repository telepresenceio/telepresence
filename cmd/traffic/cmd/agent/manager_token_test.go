package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/clog/testutil"
)

func Test_managerTokenCredentials_GetRequestMetadata(t *testing.T) {
	ctx := testutil.NewContext(t, false)

	path := filepath.Join(t.TempDir(), "manager-token")
	require.NoError(t, os.WriteFile(path, []byte("the-token-content\n"), 0o600))

	creds := newManagerTokenCredentials(path)
	md, err := creds.GetRequestMetadata(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"authorization": "Bearer the-token-content"}, md)
	require.False(t, creds.RequireTransportSecurity())
}

func Test_managerTokenCredentials_GetRequestMetadata_readFailure(t *testing.T) {
	ctx := testutil.NewContext(t, false)

	path := filepath.Join(t.TempDir(), "does-not-exist")
	creds := newManagerTokenCredentials(path)
	md, err := creds.GetRequestMetadata(ctx)
	require.NoError(t, err)
	require.Empty(t, md)
}
