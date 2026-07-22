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

const execHelperSentinel = "TELEPRESENCE_TEST_EXEC_HELPER"

// TestManagerTokenExecHelper is not a real test. When the sentinel env var is
// set it is re-executed as a fake exec credential plugin: it prints an
// ExecCredential JSON built from env vars, optionally bumping a counter file,
// and exits before the test runner emits anything else, so the captured stdout
// is exactly the JSON. Using the test binary itself keeps the fixture portable
// (a shell script is not executable on Windows).
func TestManagerTokenExecHelper(t *testing.T) {
	if os.Getenv(execHelperSentinel) != "1" {
		return
	}
	if cp := os.Getenv("EXEC_HELPER_COUNTER"); cp != "" {
		n := 0
		if b, err := os.ReadFile(cp); err == nil {
			_, _ = fmt.Sscanf(string(b), "%d", &n)
		}
		_ = os.WriteFile(cp, []byte(fmt.Sprintf("%d\n", n+1)), 0o600)
	}
	if os.Getenv("EXEC_HELPER_NO_TOKEN") == "1" {
		fmt.Println(`{"kind":"ExecCredential","apiVersion":"client.authentication.k8s.io/v1","status":{"clientCertificateData":"cert-data"}}`)
	} else {
		fmt.Printf(`{"kind":"ExecCredential","apiVersion":"client.authentication.k8s.io/v1","status":{"token":%q,"expirationTimestamp":%q}}`+"\n",
			os.Getenv("EXEC_HELPER_TOKEN"), os.Getenv("EXEC_HELPER_EXPIRY"))
	}
	os.Exit(0)
}

// execHelperConfig returns an ExecConfig that re-runs the test binary as the
// fake plugin in TestManagerTokenExecHelper, controlled by the given env vars.
func execHelperConfig(env map[string]string) *clientcmdapi.ExecConfig {
	ev := []clientcmdapi.ExecEnvVar{{Name: execHelperSentinel, Value: "1"}}
	for k, v := range env {
		ev = append(ev, clientcmdapi.ExecEnvVar{Name: k, Value: v})
	}
	return &clientcmdapi.ExecConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestManagerTokenExecHelper$"},
		Env:     ev,
	}
}

func TestExecTokenSource_CachesUntilExpiry(t *testing.T) {
	ctx := testutil.NewContext(t, false)

	counterPath := filepath.Join(t.TempDir(), "count")
	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	src := newExecTokenSource(execHelperConfig(map[string]string{
		"EXEC_HELPER_TOKEN":   "future-token",
		"EXEC_HELPER_EXPIRY":  expiry,
		"EXEC_HELPER_COUNTER": counterPath,
	}))

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

	counterPath := filepath.Join(t.TempDir(), "count")
	expiry := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	src := newExecTokenSource(execHelperConfig(map[string]string{
		"EXEC_HELPER_TOKEN":   "expired-token",
		"EXEC_HELPER_EXPIRY":  expiry,
		"EXEC_HELPER_COUNTER": counterPath,
	}))

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

	src := newExecTokenSource(execHelperConfig(map[string]string{"EXEC_HELPER_NO_TOKEN": "1"}))
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
