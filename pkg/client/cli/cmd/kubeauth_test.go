package cmd

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/authenticator"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator/patcher"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
)

func TestKubeauthPreservesCacheAndDaemons(t *testing.T) {
	quitFuncs := connect.QuitDaemonFuncs
	t.Cleanup(func() { connect.QuitDaemonFuncs = quitFuncs })
	connect.QuitDaemonFuncs = []func(context.Context){func(context.Context) {
		t.Error("credential callback must not quit daemons")
	}}

	for _, versionFileExists := range []bool{false, true} {
		name := "missing version"
		if versionFileExists {
			name = "different version"
		}
		t.Run(name, func(t *testing.T) {
			cacheDir := t.TempDir()
			marker := filepath.Join(cacheDir, "marker")
			require.NoError(t, os.WriteFile(marker, []byte("preserve"), 0o600))
			versionPath := filepath.Join(cacheDir, "version.json")
			versionData := []byte(`{"version":"999.0.0"}`)
			if versionFileExists {
				require.NoError(t, os.WriteFile(versionPath, versionData, 0o600))
			}
			cmd := kubeauthCmd()
			cmd.SetContext(filelocation.WithAppUserCacheDir(t.Context(), cacheDir))
			require.NoError(t, ensureCacheVersion(cmd, nil))
			data, err := os.ReadFile(marker)
			require.NoError(t, err)
			require.Equal(t, "preserve", string(data))
			if versionFileExists {
				data, err = os.ReadFile(versionPath)
				require.NoError(t, err)
				require.Equal(t, versionData, data)
			} else {
				require.NoFileExists(t, versionPath)
			}
		})
	}
}

type credsServer struct {
	rpc.UnimplementedAuthenticatorServer
}

func (credsServer) GetContextExecCredentials(context.Context, *rpc.GetContextExecCredentialsRequest) (*rpc.GetContextExecCredentialsResponse, error) {
	return &rpc.GetContextExecCredentialsResponse{RawCredentials: []byte("creds")}, nil
}

func TestKubeauthStubReadsTokenFromEnvironment(t *testing.T) {
	const token = "right-token"
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	svc := server.NewWithAuth(t.Context(), server.TokenInterceptors(token))
	rpc.RegisterAuthenticatorServer(svc, credsServer{})
	go func() { _ = svc.Serve(lis) }()
	t.Cleanup(svc.Stop)

	cmd := kubeauthCmd()
	cmd.SetContext(t.Context())
	require.Nil(t, cmd.Flags().Lookup("token"))

	t.Setenv(patcher.TokenEnvName, "wrong-token")
	require.Error(t, authenticateContext(cmd, []string{"ctx", lis.Addr().String()}))

	t.Setenv(patcher.TokenEnvName, token)
	require.NoError(t, authenticateContext(cmd, []string{"ctx", lis.Addr().String()}))
}
