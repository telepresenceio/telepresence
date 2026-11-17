package rootd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func TestCreateSession_refusesRequestsThatAreNotInert(t *testing.T) {
	// A kubeconfig on disk, found through KUBECONFIG, must never be used.
	dir := t.TempDir()
	onDisk := api.NewConfig()
	onDisk.CurrentContext = "ctx"
	onDisk.Contexts["ctx"] = &api.Context{Cluster: "cl"}
	onDisk.Clusters["cl"] = &api.Cluster{Server: "https://127.0.0.1:1"}
	path := filepath.Join(dir, "config")
	require.NoError(t, clientcmd.WriteToFile(*onDisk, path))
	t.Setenv("KUBECONFIG", path)

	withExec := api.NewConfig()
	withExec.CurrentContext = "ctx"
	withExec.Contexts["ctx"] = &api.Context{Cluster: "cl", AuthInfo: "u"}
	withExec.Clusters["cl"] = &api.Cluster{Server: "https://127.0.0.1:1"}
	withExec.AuthInfos["u"] = &api.AuthInfo{Exec: &api.ExecConfig{Command: "/bin/evil"}}
	execData, err := clientcmd.Write(*withExec)
	require.NoError(t, err)

	tests := []struct {
		name    string
		nc      *rpc.NetworkConfig
		wantErr string
	}{
		{
			name:    "no kubeconfig",
			nc:      &rpc.NetworkConfig{},
			wantErr: "no kubeconfig",
		},
		{
			name:    "kubeconfig flag",
			nc:      &rpc.NetworkConfig{KubeFlags: map[string]string{"kubeconfig": path}, KubeconfigData: execData},
			wantErr: `"kubeconfig"`,
		},
		{
			name:    "credential plugin",
			nc:      &rpc.NetworkConfig{KubeconfigData: execData},
			wantErr: "credential plugin",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := client.WithConfig(t.Context(), client.GetDefaultConfig())
			_, err := createSession(ctx, ctx, tt.nc, make(chan time.Time), func(context.Context) {})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.NotContains(t, err.Error(), "/bin/evil")
		})
	}
	_, statErr := os.Stat(path)
	require.NoError(t, statErr)
}
