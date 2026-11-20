package k8s

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/clog/testutil"
)

const flagTestKubeconfig = `apiVersion: v1
kind: Config
current-context: c
clusters:
- name: cl
  cluster: {server: "https://example.com:6443"}
users:
- name: u
  user: {token: t}
contexts:
- name: c
  context: {cluster: cl, user: u}
`

func TestConfigLoader_proxyURLAndImpersonateExtra(t *testing.T) {
	file := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(file, []byte(flagTestKubeconfig), 0o600))

	tests := []struct {
		name  string
		flags map[string]string
		data  []byte
	}{
		{"file", map[string]string{"kubeconfig": file}, nil},
		{"data", map[string]string{}, []byte(flagTestKubeconfig)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testutil.NewContext(t, false)
			flags := map[string]string{
				"proxy-url":     "http://proxy.example.com:3128",
				"as":            "someone",
				"as-user-extra": "scope=a,scope=b,team=x",
			}
			for k, v := range tt.flags {
				flags[k] = v
			}
			cc, err := ConfigLoader(ctx, flags, tt.data)
			require.NoError(t, err)

			rc, err := cc.ClientConfig()
			require.NoError(t, err)
			require.NotNil(t, rc.Proxy)
			req, err := http.NewRequest(http.MethodGet, "https://example.com:6443", nil)
			require.NoError(t, err)
			pu, err := rc.Proxy(req)
			require.NoError(t, err)
			require.NotNil(t, pu)
			assert.Equal(t, "http://proxy.example.com:3128", pu.String())
			assert.Equal(t, "someone", rc.Impersonate.UserName)
			assert.Equal(t, map[string][]string{"scope": {"a", "b"}, "team": {"x"}}, rc.Impersonate.Extra)

			oc, err := (&Kubeconfig{ClientConfig: cc, EffectiveFlagMap: flags}).OverridingConfig()
			require.NoError(t, err)
			raw, err := oc.MergedRawConfig()
			require.NoError(t, err)
			cl := raw.Clusters["cl"]
			require.NotNil(t, cl)
			assert.Equal(t, "http://proxy.example.com:3128", cl.ProxyURL)
			ai := raw.AuthInfos["u"]
			require.NotNil(t, ai)
			assert.Equal(t, map[string][]string{"scope": {"a", "b"}, "team": {"x"}}, ai.ImpersonateUserExtra)
		})
	}
}

func TestAppendKubeFlags_multivalued(t *testing.T) {
	args, err := AppendKubeFlags(map[string]string{"as-user-extra": "scope=a,scope=b"}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"--as-user-extra", "scope=a", "--as-user-extra", "scope=b"}, args)

	args, err = AppendKubeFlags(map[string]string{"as-group": "g1,g2"}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"--as-group", "g1", "--as-group", "g2"}, args)
}
