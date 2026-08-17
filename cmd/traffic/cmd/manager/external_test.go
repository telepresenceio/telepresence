package manager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
)

// This file covers serveExternal's startup validation: it refuses to start,
// rather than merely warn, when AuthenticationMode isn't enforcing or no
// certificate directory is configured.

func TestServeExternal_RefusesNonEnforcing(t *testing.T) {
	ctx := managerutil.WithEnv(context.Background(), &managerutil.Env{
		ExternalPort:       8443,
		ExternalTLSCertDir: "/var/run/secrets/telepresence.io/external-tls",
		AuthenticationMode: auth.ModePermissive,
	})
	err := serveExternal(ctx, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "enforcing")
}

func TestServeExternal_RefusesDisabledAuth(t *testing.T) {
	ctx := managerutil.WithEnv(context.Background(), &managerutil.Env{
		ExternalPort:       8443,
		ExternalTLSCertDir: "/var/run/secrets/telepresence.io/external-tls",
		AuthenticationMode: auth.ModeDisabled,
	})
	err := serveExternal(ctx, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "enforcing")
}

func TestServeExternal_RefusesMissingCertDir(t *testing.T) {
	ctx := managerutil.WithEnv(context.Background(), &managerutil.Env{
		ExternalPort:       8443,
		AuthenticationMode: auth.ModeEnforcing,
	})
	err := serveExternal(ctx, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EXTERNAL_TLS_CERT_DIR")
}
