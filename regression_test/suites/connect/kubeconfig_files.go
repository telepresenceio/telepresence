package connect

import (
	"os"
	"path/filepath"

	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// KubeconfigFiles proves that a kubeconfig that refers to files connects in
// host mode. The root daemon refuses such a kubeconfig, so the user daemon
// has to hand it a copy where every file is replaced by its content.
type KubeconfigFiles struct {
	rt.Suite
}

func init() {
	rt.Register(&KubeconfigFiles{}, rt.InArea("connect"), rt.NeedsManager(managers.Default))
}

// Test_FileReferences connects through a copy of the run's kubeconfig where the
// cluster's certificate authority, and the user's client certificate and key
// when it has them, are files instead of inline data.
func (s *KubeconfigFiles) Test_FileReferences() {
	t := s.T()
	ns := s.AppNamespace()
	env := rt.Env{Ctx: s.Ctx(), T: t, R: s.R()}
	dir := s.R().ArtifactDir("kubeconfig-files", "file-references")

	var (
		converted []string
		buildErr  error
	)
	writeFile := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil && buildErr == nil {
			buildErr = err
		}
		return p
	}
	path, err := rt.KubeConfigCopy(env, func(cfg *api.Config) {
		cc := cfg.Contexts[cfg.CurrentContext]
		if cc == nil {
			return
		}
		if cluster := cfg.Clusters[cc.Cluster]; cluster != nil && len(cluster.CertificateAuthorityData) > 0 {
			cluster.CertificateAuthority = writeFile("ca.crt", cluster.CertificateAuthorityData)
			cluster.CertificateAuthorityData = nil
			converted = append(converted, "certificate-authority")
		}
		// A user with a token or a credential plugin has no certificate to put in a file.
		if user := cfg.AuthInfos[cc.AuthInfo]; user != nil && len(user.ClientCertificateData) > 0 && len(user.ClientKeyData) > 0 {
			user.ClientCertificate = writeFile("client.crt", user.ClientCertificateData)
			user.ClientCertificateData = nil
			user.ClientKey = writeFile("client.key", user.ClientKeyData)
			user.ClientKeyData = nil
			converted = append(converted, "client-certificate", "client-key")
		}
	})
	s.Require().NoError(err)
	s.Require().NoError(buildErr)
	if len(converted) == 0 {
		t.Skip("skipping: the run's kubeconfig has no certificate authority data or client certificate to move into files")
	}
	t.Logf("kubeconfig files for: %v", converted)

	freeDefaultConnection(t, ns)
	conn := s.Connect(rt.ConnWithKubeconfig(path))
	t.Cleanup(func() { conn.Disconnect(t) })

	st := conn.Status(t)
	s.True(st.UserDaemon.Running, "user daemon should be running after connect")
	s.True(st.RootDaemon.Running, "root daemon should be running after connect")
	s.Equal(ns, st.UserDaemon.Namespace)
}
