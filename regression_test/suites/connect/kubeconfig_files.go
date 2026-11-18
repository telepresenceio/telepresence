package connect

import (
	"os"
	"path/filepath"

	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
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

// moveCredentialsToFiles returns a copy of the run's kubeconfig where the
// cluster's certificate authority data, and the user's client certificate and
// key when it has them, are written to files under dir. With refs, the copy
// refers to the files; otherwise the data is removed. It returns the
// kubeconfig path, the kubectl flag names of the moved items (certificate-authority,
// client-certificate, client-key) and the file for each flag.
func (s *KubeconfigFiles) moveCredentialsToFiles(dir string, refs bool) (string, []string, map[string]string) {
	var (
		flags    []string
		files    = map[string]string{}
		buildErr error
	)
	writeFile := func(flag, name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil && buildErr == nil {
			buildErr = err
		}
		flags = append(flags, flag)
		files[flag] = p
		return p
	}
	ref := func(p string) string {
		if refs {
			return p
		}
		return ""
	}
	env := rt.Env{Ctx: s.Ctx(), T: s.T(), R: s.R()}
	path, err := rt.KubeConfigCopy(env, func(cfg *api.Config) {
		cc := cfg.Contexts[cfg.CurrentContext]
		if cc == nil {
			return
		}
		if cluster := cfg.Clusters[cc.Cluster]; cluster != nil && len(cluster.CertificateAuthorityData) > 0 {
			cluster.CertificateAuthority = ref(writeFile("certificate-authority", "ca.crt", cluster.CertificateAuthorityData))
			cluster.CertificateAuthorityData = nil
		}
		// A user with a token or a credential plugin has no certificate to put in a file.
		if user := cfg.AuthInfos[cc.AuthInfo]; user != nil && len(user.ClientCertificateData) > 0 && len(user.ClientKeyData) > 0 {
			user.ClientCertificate = ref(writeFile("client-certificate", "client.crt", user.ClientCertificateData))
			user.ClientCertificateData = nil
			user.ClientKey = ref(writeFile("client-key", "client.key", user.ClientKeyData))
			user.ClientKeyData = nil
		}
	})
	s.Require().NoError(err)
	s.Require().NoError(buildErr)
	if len(flags) == 0 {
		s.T().Skip("skipping: the run's kubeconfig has no certificate authority data or client certificate to move into files")
	}
	return path, flags, files
}

// Test_FileReferences connects through a copy of the run's kubeconfig where the
// cluster's certificate authority, and the user's client certificate and key
// when it has them, are files instead of inline data.
func (s *KubeconfigFiles) Test_FileReferences() {
	t := s.T()
	ns := s.AppNamespace()
	path, converted, _ := s.moveCredentialsToFiles(s.R().ArtifactDir("kubeconfig-files", "file-references"), true)
	t.Logf("kubeconfig files for: %v", converted)

	freeDefaultConnection(t, ns)
	conn := s.Connect(rt.ConnWithKubeconfig(path))
	t.Cleanup(func() { conn.Disconnect(t) })

	st := conn.Status(t)
	s.True(st.UserDaemon.Running, "user daemon should be running after connect")
	s.True(st.RootDaemon.Running, "root daemon should be running after connect")
	s.Equal(ns, st.UserDaemon.Namespace)
}

// Test_FileFlags connects with a kubeconfig that lacks the cluster's
// certificate authority and the user's client certificate and key, and passes
// those as --certificate-authority, --client-certificate and --client-key. The
// kubeconfig cannot connect without the flags, and a routed service shows that
// the root daemon's own cluster client received them.
func (s *KubeconfigFiles) Test_FileFlags() {
	t := s.T()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("file-flags"))
	path, flags, files := s.moveCredentialsToFiles(s.R().ArtifactDir("kubeconfig-files", "file-flags"), false)
	t.Logf("kubeconfig flags: %v", flags)

	args := []string{"--kubeconfig", path}
	for _, f := range flags {
		args = append(args, "--"+f, files[f])
	}
	freeDefaultConnection(t, ns)
	conn := s.Connect(rt.ConnExtraArgs(args...))
	t.Cleanup(func() { conn.Disconnect(t) })

	st := conn.Status(t)
	s.True(st.UserDaemon.Running, "user daemon should be running after connect")
	s.True(st.RootDaemon.Running, "root daemon should be running after connect")
	s.Equal(ns, st.UserDaemon.Namespace)
	rt.RoutedToCluster(t, wl.ServiceURL())
}
