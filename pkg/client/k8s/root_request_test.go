package k8s

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/clog/testutil"
)

func goodRootConfig() *api.Config {
	cfg := api.NewConfig()
	cfg.CurrentContext = "ctx"
	cfg.Contexts["ctx"] = &api.Context{Cluster: "cl", AuthInfo: "user"}
	cfg.Clusters["cl"] = &api.Cluster{Server: "https://127.0.0.1:6443", CertificateAuthorityData: []byte("CA")}
	cfg.AuthInfos["user"] = &api.AuthInfo{Token: "tok", ClientCertificateData: []byte("CERT"), ClientKeyData: []byte("KEY")}
	return cfg
}

func writeRootConfig(t *testing.T, cfg *api.Config) []byte {
	t.Helper()
	data, err := clientcmd.Write(*cfg)
	require.NoError(t, err)
	return data
}

func TestCheckRootRequest_good(t *testing.T) {
	require.NoError(t, CheckRootRequest(nil, writeRootConfig(t, goodRootConfig())))
	require.NoError(t, CheckRootRequest(map[string]string{"request-timeout": "30s"}, writeRootConfig(t, goodRootConfig())))
}

func TestCheckRootRequest_refused(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(cfg *api.Config)
		wantErr string
	}{
		{
			name:    "credential plugin",
			mutate:  func(cfg *api.Config) { cfg.AuthInfos["user"].Exec = &api.ExecConfig{Command: "/bin/evil"} },
			wantErr: `user "user" refers to a credential plugin`,
		},
		{
			name:    "auth provider",
			mutate:  func(cfg *api.Config) { cfg.AuthInfos["user"].AuthProvider = &api.AuthProviderConfig{Name: "gcp"} },
			wantErr: `user "user" refers to an auth provider`,
		},
		{
			name:    "token file",
			mutate:  func(cfg *api.Config) { cfg.AuthInfos["user"].TokenFile = "/etc/shadow" },
			wantErr: `user "user" refers to a token file`,
		},
		{
			name: "client certificate file",
			mutate: func(cfg *api.Config) {
				cfg.AuthInfos["user"].ClientCertificateData = nil
				cfg.AuthInfos["user"].ClientCertificate = "/etc/shadow"
			},
			wantErr: `user "user" refers to a client certificate file`,
		},
		{
			name: "client key file",
			mutate: func(cfg *api.Config) {
				cfg.AuthInfos["user"].ClientKeyData = nil
				cfg.AuthInfos["user"].ClientKey = "/etc/shadow"
			},
			wantErr: `user "user" refers to a client key file`,
		},
		{
			name: "certificate authority file",
			mutate: func(cfg *api.Config) {
				cfg.Clusters["cl"].CertificateAuthorityData = nil
				cfg.Clusters["cl"].CertificateAuthority = "/etc/shadow"
			},
			wantErr: `cluster "cl" refers to a certificate authority file`,
		},
		{
			name: "plugin in an entry outside the current context",
			mutate: func(cfg *api.Config) {
				cfg.AuthInfos["other"] = &api.AuthInfo{Exec: &api.ExecConfig{Command: "/bin/evil"}}
				cfg.Contexts["other"] = &api.Context{Cluster: "cl", AuthInfo: "other"}
			},
			wantErr: `user "other" refers to a credential plugin`,
		},
		{
			name: "file in a cluster outside the current context",
			mutate: func(cfg *api.Config) {
				cfg.Clusters["other"] = &api.Cluster{Server: "https://example.com", CertificateAuthority: "/etc/shadow"}
			},
			wantErr: `cluster "other" refers to a certificate authority file`,
		},
		{
			name:    "no server",
			mutate:  func(cfg *api.Config) { cfg.Clusters["cl"].Server = "" },
			wantErr: `cluster "cl" in the kubeconfig has no server`,
		},
		{
			name:    "no current context",
			mutate:  func(cfg *api.Config) { cfg.CurrentContext = "" },
			wantErr: "no current context",
		},
		{
			name:    "current context does not exist",
			mutate:  func(cfg *api.Config) { cfg.CurrentContext = "nope" },
			wantErr: `current context "nope" does not exist`,
		},
		{
			name:    "cluster of the context does not exist",
			mutate:  func(cfg *api.Config) { cfg.Contexts["ctx"].Cluster = "nope" },
			wantErr: `cluster "nope" of context "ctx" does not exist`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := goodRootConfig()
			tt.mutate(cfg)
			err := CheckRootRequest(nil, writeRootConfig(t, cfg))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.NotContains(t, err.Error(), "/etc/shadow")
			assert.NotContains(t, err.Error(), "/bin/evil")
		})
	}
}

func TestCheckRootRequest_noKubeconfig(t *testing.T) {
	err := CheckRootRequest(nil, nil)
	require.ErrorContains(t, err, "no kubeconfig")
	err = CheckRootRequest(nil, []byte{})
	require.ErrorContains(t, err, "no kubeconfig")
}

func TestCheckRootRequest_unparsable(t *testing.T) {
	err := CheckRootRequest(nil, []byte("secret-token: [valid"))
	require.ErrorContains(t, err, "cannot be parsed")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestCheckRootRequest_flags(t *testing.T) {
	data := writeRootConfig(t, goodRootConfig())
	for _, flag := range []string{
		"kubeconfig", "certificate-authority", "client-certificate", "client-key", "token", "server",
		"as", "context", "namespace", "cache-dir", "no-such-flag",
	} {
		t.Run(flag, func(t *testing.T) {
			err := CheckRootRequest(map[string]string{flag: "/etc/shadow"}, data)
			require.Error(t, err)
			assert.Contains(t, err.Error(), `"`+flag+`"`)
			assert.NotContains(t, err.Error(), "/etc/shadow")
		})
	}
}

func TestCheckRootRequest_badTimeout(t *testing.T) {
	err := CheckRootRequest(map[string]string{"request-timeout": "soon"}, writeRootConfig(t, goodRootConfig()))
	require.ErrorContains(t, err, "request-timeout")
}

func TestRootKubeFlags(t *testing.T) {
	assert.Nil(t, RootKubeFlags(nil))
	assert.Nil(t, RootKubeFlags(map[string]string{"kubeconfig": "/x", "token": "y"}))
	assert.Equal(t, map[string]string{"request-timeout": "5s"},
		RootKubeFlags(map[string]string{"kubeconfig": "/x", "request-timeout": "5s", "as": "me"}))
}

func TestRootKubeFlags_acceptedByCheck(t *testing.T) {
	flags := map[string]string{"request-timeout": "5s", "kubeconfig": "/x", "as": "me"}
	require.NoError(t, CheckRootRequest(RootKubeFlags(flags), writeRootConfig(t, goodRootConfig())))
}

func TestNewRootKubeconfig_requiresKubeconfig(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "does-not-matter"))
	_, err := NewRootKubeconfig(ctx, nil, "", nil, nil)
	require.ErrorContains(t, err, "requires a kubeconfig")
}

func TestNewRootKubeconfig_rejectsClusterWithoutServer(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	cfg := goodRootConfig()
	cfg.Clusters["cl"].Server = ""
	_, err := NewRootKubeconfig(ctx, nil, "", writeRootConfig(t, cfg), nil)
	require.ErrorContains(t, err, "no server")
}

func TestKubeconfig_OverridingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	cfg := goodRootConfig()
	cfg.Contexts["ctx2"] = &api.Context{Cluster: "cl2", AuthInfo: "user"}
	cfg.Clusters["cl2"] = &api.Cluster{Server: "https://cl2.example.com"}
	require.NoError(t, os.WriteFile(path, writeRootConfig(t, cfg), 0o600))

	flags := map[string]string{
		"kubeconfig": path,
		"context":    "ctx2",
		"token":      "flag-token",
		"as":         "someone",
		"as-group":   "g1,g2",
		"namespace":  "ns1",
		"server":     "https://flag.example.com",
	}
	configFlags, err := ConfigFlags(flags)
	require.NoError(t, err)
	kf := &Kubeconfig{ClientConfig: configFlags.ToRawKubeConfigLoader(), EffectiveFlagMap: flags}

	oc, err := kf.OverridingConfig()
	require.NoError(t, err)
	merged, err := oc.MergedRawConfig()
	require.NoError(t, err)

	assert.Equal(t, "ctx2", merged.CurrentContext)
	assert.Equal(t, "ns1", merged.Contexts["ctx2"].Namespace)
	assert.Equal(t, "https://flag.example.com", merged.Clusters["cl2"].Server)
	ai := merged.AuthInfos["user"]
	require.NotNil(t, ai)
	assert.Equal(t, "flag-token", ai.Token)
	assert.Equal(t, "someone", ai.Impersonate)
	assert.Equal(t, []string{"g1", "g2"}, ai.ImpersonateGroups)
}

func TestManagerServerCAPEM(t *testing.T) {
	pem, err := ManagerServerCAPEM("")
	require.NoError(t, err)
	assert.Empty(t, pem)

	pem, err = ManagerServerCAPEM("-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----\n")
	require.NoError(t, err)
	assert.Contains(t, pem, "BEGIN CERTIFICATE")

	file := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(file, []byte("-----BEGIN CERTIFICATE-----\nfrom-file\n-----END CERTIFICATE-----\n"), 0o600))
	pem, err = ManagerServerCAPEM(file)
	require.NoError(t, err)
	assert.Contains(t, pem, "from-file")
}

func TestNewRootKubeconfig_appliesRequestTimeout(t *testing.T) {
	ctx := testutil.NewContext(t, false)
	cfg := goodRootConfig()
	cfg.Clusters["cl"].CertificateAuthorityData = nil
	cfg.Clusters["cl"].InsecureSkipTLSVerify = true
	cfg.AuthInfos["user"] = &api.AuthInfo{Token: "tok"}
	kc, err := NewRootKubeconfig(ctx, map[string]string{"request-timeout": "30s"}, "", writeRootConfig(t, cfg), nil)
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, kc.RestConfig.Timeout)
	assert.Equal(t, "https://127.0.0.1:6443", kc.RestConfig.Host)
}
