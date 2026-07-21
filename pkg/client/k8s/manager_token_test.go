package k8s

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/clog/testutil"
)

func TestNewManagerTokenSource_StaticBearerToken(t *testing.T) {
	ctx := testutil.NewContext(t, false)

	kc := &Kubeconfig{RestConfig: &rest.Config{BearerToken: "static-tok"}}
	src := newManagerTokenSource(kc)
	require.NotNil(t, src)

	md, err := newManagerTokenCredentials(src).GetRequestMetadata(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"authorization": "Bearer static-tok"}, md)
}

func TestNewManagerTokenSource_BearerTokenFile(t *testing.T) {
	ctx := testutil.NewContext(t, false)

	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte("file-tok\n"), 0o600))

	kc := &Kubeconfig{RestConfig: &rest.Config{BearerTokenFile: path}}
	src := newManagerTokenSource(kc)
	require.NotNil(t, src)

	md, err := newManagerTokenCredentials(src).GetRequestMetadata(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"authorization": "Bearer file-tok"}, md)
}

func TestNewManagerTokenSource_CertOnly(t *testing.T) {
	kc := &Kubeconfig{RestConfig: &rest.Config{}}
	require.Nil(t, newManagerTokenSource(kc))
}

func TestNewManagerTokenSource_AuthProviderUnsupported(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	kc := &Kubeconfig{
		Context:    ctx,
		RestConfig: &rest.Config{AuthProvider: &clientcmdapi.AuthProviderConfig{Name: "gcp"}},
	}
	require.Nil(t, newManagerTokenSource(kc))
}

// writeExecScript writes a fake exec credential plugin to t.TempDir() that
// echoes an ExecCredential JSON for token/expiry and records how many times
// it has run in a counter file.
func writeExecScript(t *testing.T, counterPath, token, expiry string) string {
	t.Helper()
	scriptPath := filepath.Join(t.TempDir(), "kubeauth-stub.sh")
	script := fmt.Sprintf(`#!/bin/sh
n=0
if [ -f %q ]; then n=$(cat %q); fi
n=$((n+1))
echo "$n" > %q
cat <<JSON
{"kind":"ExecCredential","apiVersion":"client.authentication.k8s.io/v1","status":{"token":%q,"expirationTimestamp":%q}}
JSON
`, counterPath, counterPath, counterPath, token, expiry)
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))
	return scriptPath
}

func TestExecTokenSource_CachesUntilExpiry(t *testing.T) {
	ctx := testutil.NewContext(t, false)

	dir := t.TempDir()
	counterPath := filepath.Join(dir, "count")
	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	script := writeExecScript(t, counterPath, "future-token", expiry)

	src := newExecTokenSource(&clientcmdapi.ExecConfig{Command: script})

	tok, err := src.Token(ctx)
	require.NoError(t, err)
	require.Equal(t, "future-token", tok)

	tok, err = src.Token(ctx)
	require.NoError(t, err)
	require.Equal(t, "future-token", tok)

	data, err := os.ReadFile(counterPath)
	require.NoError(t, err)
	require.Equal(t, "1\n", string(data), "plugin should only run once while the token is still valid")
}

func TestExecTokenSource_ExpiredTokenTriggersReExecution(t *testing.T) {
	ctx := testutil.NewContext(t, false)

	dir := t.TempDir()
	counterPath := filepath.Join(dir, "count")
	expiry := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	script := writeExecScript(t, counterPath, "expired-token", expiry)

	src := newExecTokenSource(&clientcmdapi.ExecConfig{Command: script})

	_, err := src.Token(ctx)
	require.NoError(t, err)
	_, err = src.Token(ctx)
	require.NoError(t, err)

	data, err := os.ReadFile(counterPath)
	require.NoError(t, err)
	require.Equal(t, "2\n", string(data), "plugin should re-run once the cached token has expired")
}

func TestExecTokenSource_NoBearerToken(t *testing.T) {
	ctx := testutil.NewContext(t, false)

	scriptPath := filepath.Join(t.TempDir(), "kubeauth-stub.sh")
	script := `#!/bin/sh
cat <<'JSON'
{"kind":"ExecCredential","apiVersion":"client.authentication.k8s.io/v1","status":{"clientCertificateData":"cert-data"}}
JSON
`
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))

	src := newExecTokenSource(&clientcmdapi.ExecConfig{Command: scriptPath})
	tok, err := src.Token(ctx)
	require.Empty(t, tok)
	require.ErrorIs(t, err, errNoBearerToken)

	// The credentials wrapper must treat this as "no token available", not a
	// hard failure: empty metadata, nil error.
	md, err := newManagerTokenCredentials(src).GetRequestMetadata(ctx)
	require.NoError(t, err)
	require.Empty(t, md)
}

func TestManagerTokenCredentials_RequireTransportSecurity(t *testing.T) {
	creds := newManagerTokenCredentials(staticTokenSource("x"))
	require.False(t, creds.RequireTransportSecurity())
}
