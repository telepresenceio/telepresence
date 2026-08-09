package auth

import (
	"archive/zip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json/v2"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	grpcClient "github.com/telepresenceio/telepresence/v2/pkg/grpc/client"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// externalEndpointSecretName is the kubernetes.io/tls Secret the tests
// generate a CA/cert for and point externalEndpoint.tls.secretName at.
const externalEndpointSecretName = "rtest-external-tls"

// externalEndpointServiceName is the chart-rendered Service fronting the
// external listener (charts/telepresence-oss/templates/external-endpoint.yaml).
const externalEndpointServiceName = "traffic-manager-external"

// externalEndpointContainerPort is the container port the external TLS
// listener binds to (values.yaml's externalEndpoint.port default).
const externalEndpointContainerPort = 8444

// externalEndpointSpec returns managers.AuthEnforcing() with the external
// TLS gRPC listener enabled on a NodePort Service, terminated with an
// existing kubernetes.io/tls Secret named secretName.
func externalEndpointSpec(secretName string) managers.Spec {
	v := managers.AuthEnforcing().Values
	v.ExternalEndpoint = managers.ExternalEndpoint{
		Enabled: true,
		Port:    externalEndpointContainerPort,
		Service: managers.ExternalEndpointService{Type: "NodePort", Port: 443},
		TLS:     managers.ExternalEndpointTLS{SecretName: secretName},
	}
	// The external TLS listener is the control plane only. Agent-bound
	// streams (intercepted-traffic delivery) have no Kubernetes
	// port-forward in external mode, so the QUIC tunnel carries them --
	// same NodePort arrangement as the quic area's specs.
	v.QuicTunnel = managers.QuicTunnel{
		Enabled: true,
		Service: managers.QuicTunnelService{Type: "NodePort", NodePort: managers.QuicNodePortPort},
	}
	return managers.Spec{Key: "external-endpoint", Values: v}
}

// nodeInternalIP returns the first node's IPv4 InternalIP, the address a
// NodePort Service is reachable at from the host on the kind test cluster.
func nodeInternalIP(t *testing.T, ctx context.Context, r *rt.Runtime) string {
	t.Helper()
	out, err := r.Kubectl(ctx, "", "get", "nodes", "-o",
		`jsonpath={.items[0].status.addresses[?(@.type=="InternalIP")].address}`)
	if err != nil {
		t.Fatalf("getting node InternalIP: %v", err)
	}
	for _, addr := range strings.Fields(out) {
		if ip := net.ParseIP(addr); ip != nil && ip.To4() != nil {
			return addr
		}
	}
	t.Fatalf("cluster reported no IPv4 node InternalIP in %q", out)
	return ""
}

// generateExternalEndpointCert creates a self-signed CA and a leaf
// certificate/key whose only SAN is nodeIP, writes all three PEM files under
// dir, and returns their paths.
func generateExternalEndpointCert(t *testing.T, dir, nodeIP string) (caPath, certPath, keyPath string) {
	t.Helper()
	ip := net.ParseIP(nodeIP)
	if ip == nil {
		t.Fatalf("node InternalIP %q did not parse", nodeIP)
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "rtest external-endpoint CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("creating CA certificate: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parsing CA certificate: %v", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating leaf key: %v", err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: nodeIP},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{ip},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("creating leaf certificate: %v", err)
	}
	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatalf("marshaling leaf key: %v", err)
	}

	caPath = filepath.Join(dir, "ca.pem")
	certPath = filepath.Join(dir, "tls.crt")
	keyPath = filepath.Join(dir, "tls.key")
	writePEMFile(t, caPath, "CERTIFICATE", caDER)
	writePEMFile(t, certPath, "CERTIFICATE", leafDER)
	writePEMFile(t, keyPath, "EC PRIVATE KEY", leafKeyDER)
	return caPath, certPath, keyPath
}

// writePEMFile PEM-encodes der as blockType and writes it to path.
func writePEMFile(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		t.Fatalf("encoding %s: %v", path, err)
	}
}

// createExternalEndpointSecret (re)creates the kubernetes.io/tls Secret
// externalEndpointSecretName in the manager namespace from certPath/keyPath,
// deleting any stale leftover first, and registers a t.Cleanup to remove it.
func createExternalEndpointSecret(t *testing.T, ctx context.Context, r *rt.Runtime, certPath, keyPath string) {
	t.Helper()
	mgrNS := managers.ManagerNamespace
	deleteExternalEndpointSecret(t, ctx, r)
	if _, err := r.Kubectl(ctx, mgrNS, "create", "secret", "tls", externalEndpointSecretName,
		"--cert", certPath, "--key", keyPath); err != nil {
		t.Fatalf("creating %s secret: %v", externalEndpointSecretName, err)
	}
	t.Cleanup(func() { deleteExternalEndpointSecret(t, ctx, r) })
}

func deleteExternalEndpointSecret(t *testing.T, ctx context.Context, r *rt.Runtime) {
	t.Helper()
	if _, err := r.Kubectl(ctx, managers.ManagerNamespace, "delete", "secret", externalEndpointSecretName,
		"--ignore-not-found"); err != nil {
		t.Fatalf("deleting %s secret: %v", externalEndpointSecretName, err)
	}
}

// externalEndpointNodePort reads back the NodePort the chart's Service was
// assigned: externalEndpointSpec sets no static nodePort, so it is only
// known after the Service exists.
func externalEndpointNodePort(t *testing.T, ctx context.Context, r *rt.Runtime) int {
	t.Helper()
	out, err := r.Kubectl(ctx, managers.ManagerNamespace, "get", "svc", externalEndpointServiceName,
		"-o", "jsonpath={.spec.ports[0].nodePort}")
	if err != nil {
		t.Fatalf("getting %s nodePort: %v", externalEndpointServiceName, err)
	}
	np, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("parsing %s nodePort %q: %v", externalEndpointServiceName, out, err)
	}
	return np
}

// setupExternalEndpoint provisions the external-endpoint manager spec end to
// end and returns the node IP, NodePort, and CA PEM path.
func setupExternalEndpoint(t *testing.T, ctx context.Context, r *rt.Runtime) (nodeIP string, nodePort int, caPath string) {
	t.Helper()
	return setupExternalEndpointSpec(t, ctx, r, externalEndpointSpec)
}

// setupExternalEndpointSpec is setupExternalEndpoint parameterized on the
// manager spec builder.
func setupExternalEndpointSpec(
	t *testing.T, ctx context.Context, r *rt.Runtime, specFn func(secretName string) managers.Spec,
) (nodeIP string, nodePort int, caPath string) {
	t.Helper()
	nodeIP = nodeInternalIP(t, ctx, r)
	dir := t.TempDir()
	caPath, certPath, keyPath := generateExternalEndpointCert(t, dir, nodeIP)
	createExternalEndpointSecret(t, ctx, r, certPath, keyPath)
	rt.Mutate(t, rt.ManagerFixture(specFn(externalEndpointSecretName)))
	nodePort = externalEndpointNodePort(t, ctx, r)
	return nodeIP, nodePort, caPath
}

// buildBlackholedExternalKubeconfig derives a kubeconfig authenticating as
// tok, with the Kubernetes API server rewritten to an unreachable address
// and the telepresence.io extension pointing at the external endpoint.
func buildBlackholedExternalKubeconfig(env rt.Env, name, tok, managerAddress, caPath string) (string, error) {
	env.T.Helper()
	extData, err := json.Marshal(map[string]any{
		"cluster": map[string]any{
			"managerAddress":  managerAddress,
			"managerServerCA": caPath,
		},
	})
	if err != nil {
		return "", err
	}
	var buildErr error
	path, err := rt.KubeConfigCopy(env, func(cfg *api.Config) {
		cc := cfg.Contexts[cfg.CurrentContext]
		if cc == nil {
			buildErr = fmt.Errorf("current context %q not found in kubeconfig", cfg.CurrentContext)
			return
		}
		authInfoName := name + "-token"
		cfg.AuthInfos[authInfoName] = &api.AuthInfo{Token: tok}
		derived := cc.DeepCopy()
		derived.AuthInfo = authInfoName
		ctxName := name + "-ctx"
		cfg.Contexts[ctxName] = derived
		cfg.CurrentContext = ctxName

		cluster := cfg.Clusters[cc.Cluster]
		if cluster == nil {
			buildErr = fmt.Errorf("cluster %q not found in kubeconfig", cc.Cluster)
			return
		}
		cluster.Server = "https://127.0.0.1:1"
		cluster.InsecureSkipTLSVerify = true
		cluster.CertificateAuthority = ""
		cluster.CertificateAuthorityData = nil
		if cluster.Extensions == nil {
			cluster.Extensions = map[string]k8sruntime.Object{}
		}
		cluster.Extensions["telepresence.io"] = &k8sruntime.Unknown{Raw: extData}
	})
	if err != nil {
		return "", err
	}
	if buildErr != nil {
		return "", buildErr
	}
	return path, nil
}

// appNamespaceAttachmentRules is the RBAC an identity needs in a namespace
// it intercepts in: kind-qualified attachments plus diagnostic logs access.
const appNamespaceAttachmentRules = `  - apiGroups: ["telepresence.io"]
    resources: ["attachments/deployment"]
    verbs: ["create", "get"]
  - apiGroups: ["telepresence.io"]
    resources: ["logs", "logs/yaml"]
    verbs: ["get"]`

// namespaceGrantManifest is a Role and RoleBinding named %[1]s in namespace
// %[2]s granting %[4]s to the ServiceAccount %[1]s in namespace %[3]s.
const namespaceGrantManifest = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: %[1]s
  namespace: %[2]s
rules:
%[4]s
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: %[1]s
  namespace: %[2]s
subjects:
  - kind: ServiceAccount
    name: %[1]s
    namespace: %[3]s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: %[1]s
`

// grantInNamespace applies namespaceGrantManifest for name in ns and
// registers a t.Cleanup removing it.
func grantInNamespace(t *testing.T, ctx context.Context, r *rt.Runtime, name, ns, rulesYAML string) {
	t.Helper()
	manifest := fmt.Sprintf(namespaceGrantManifest, name, ns, managers.ManagerNamespace, rulesYAML)
	path := filepath.Join(t.TempDir(), name+"-"+ns+".yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing namespace grant manifest for %s: %v", name, err)
	}
	if _, err := r.Kubectl(ctx, ns, "apply", "-f", path); err != nil {
		t.Fatalf("applying namespace grant for %s in %s: %v", name, ns, err)
	}
	t.Cleanup(func() {
		for _, kind := range []string{"rolebinding", "role"} {
			_, _ = r.Kubectl(ctx, ns, "delete", kind, name, "--ignore-not-found")
		}
	})
}

// externalRolloutTimeout/externalRolloutPoll bound post-restart recovery
// polls.
const (
	externalRolloutTimeout = 2 * time.Minute
	externalRolloutPoll    = 3 * time.Second
)

// externalBlackholeEnvKey/externalBlackholeEnvValue are a declared env var
// read back through the intercept's --env-file.
const (
	externalBlackholeEnvKey   = "RTEST_EXTERNAL_MARKER"
	externalBlackholeEnvValue = "external-blackhole-agent-env"
)

// ExternalEndpointBlackhole proves that with the Kubernetes API server
// unreachable from the client, a full connect/intercept/gather-logs/
// reconnect/quit lifecycle still succeeds over the external TLS listener.
type ExternalEndpointBlackhole struct {
	rt.Suite
}

func init() {
	rt.Register(&ExternalEndpointBlackhole{}, rt.InArea("auth"), rt.NeedsManager(managers.Default), rt.WithLabels(rt.Slow))
}

func (s *ExternalEndpointBlackhole) Test_ExternalEndpointBlackholedAPIServer() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()

	nodeIP, nodePort, caPath := setupExternalEndpoint(t, ctx, r)
	managerAddress := "tls://" + net.JoinHostPort(nodeIP, strconv.Itoa(nodePort))

	// telepresenceGrantWithLogsRules (gate.go): connections create,
	// attachments create/get, logs get -- no pods/portforward at all. The
	// external path must not need it.
	name := createGateIdentity(t, ctx, r, "rtest-external-blackhole", telepresenceGrantWithLogsRules)
	// Attachment reviews happen in the WORKLOAD's namespace with the kind as
	// a subresource, so the identity also needs kind-qualified attachment
	// (and diagnostic) grants where the Echo Deployment lives.
	grantInNamespace(t, ctx, r, name, ns, appNamespaceAttachmentRules)
	tok := kubectlCreateToken(t, ctx, r, name)

	path, err := buildBlackholedExternalKubeconfig(rt.Env{Ctx: ctx, T: t, R: r}, name, tok, managerAddress, caPath)
	s.Require().NoError(err)
	tp := r.CLIWithEnv(map[string]string{"KUBECONFIG": path})

	tpl := workloads.Echo("external-blackhole")
	tpl.Env = map[string]string{externalBlackholeEnvKey: externalBlackholeEnvValue}
	wl := s.Workload(tpl)
	ls := s.LocalEcho()

	freeDefaultConnection(t, ns)
	quitDefensively(t, r, ctx, "ExternalEndpointBlackhole:Test_ExternalEndpointBlackholedAPIServer")

	_, stderr, err := tp.Run(ctx, "connect", "--namespace", ns, "--manager-namespace", managers.ManagerNamespace)
	s.Require().NoError(err, "connect over the external endpoint with the API server blackholed: %s", stderr)

	var st cli.Status
	s.Require().NoError(tp.JSON(ctx, &st, "status", "--format", "json"))
	s.True(st.UserDaemon.Running, "user daemon should be running")
	s.NotEmpty(st.TrafficManager.Name, "status should report a connected traffic manager")

	var entries []cli.ListEntry
	s.Require().NoError(tp.JSON(ctx, &entries, "list", "--format", "json", "-n", ns))
	found := false
	for _, e := range entries {
		if e.Name == wl.Name && e.Namespace == ns {
			found = true
			break
		}
	}
	s.True(found, "list should show %s", wl.Name)

	envFile := filepath.Join(t.TempDir(), "external-blackhole-env.sh")
	interceptOpts := append(rt.ToLocal(ls, "http")(), cli.MountFalse()()...)
	interceptOpts = append(interceptOpts, cli.EnvFile(envFile)()...)
	iArgs := append([]string{"intercept", wl.Name, "--namespace", ns, "--format", "json", "--detailed-output"}, interceptOpts...)
	_, stderr, err = tp.Run(ctx, iArgs...)
	s.Require().NoError(err, "intercept: %s", stderr)
	t.Cleanup(func() { _, _, _ = tp.Run(ctx, "detach", wl.Name, "-n", ns) })

	rt.RoutedToLocal(t, wl.ServiceURL(), ls)

	// Agent-bound operation beyond intercepted-traffic delivery: the
	// intercept's --env-file is populated from the agent's reported
	// environment (ReviewIntercept), which only reaches the client over
	// the same external control connection RoutedToLocal already proved
	// carries traffic -- so this proves a second, independent agent-bound
	// data path (environment retrieval, not data-plane delivery) also
	// works with the API server blackholed.
	var envContent string
	s.Require().Eventually(func() bool {
		data, err := os.ReadFile(envFile)
		if err != nil {
			return false
		}
		envContent = string(data)
		return true
	}, externalRolloutTimeout, externalRolloutPoll, "env file %s never appeared", envFile)
	s.Contains(envContent, externalBlackholeEnvKey+"="+externalBlackholeEnvValue)

	outFile := filepath.Join(t.TempDir(), "gather-logs.zip")
	_, stderr, err = tp.Run(ctx, "gather-logs", "--output-file", outFile, "--traffic-agents=None")
	s.Require().NoError(err, "gather-logs: %s", stderr)
	s.True(zipHasManagerLog(t, outFile), "gather-logs zip should contain a traffic-manager log")

	// Restart the manager with the RUN's own kubeconfig, not the blackholed
	// one: RestartManager uses r.Kubectl directly, independent of tp's
	// KUBECONFIG override.
	s.Require().NoError(rt.RestartManager(rt.Env{Ctx: ctx, T: t, R: r}))

	s.Require().Eventually(func() bool {
		var st2 cli.Status
		if err := tp.JSON(ctx, &st2, "status", "--format", "json"); err != nil {
			return false
		}
		return st2.UserDaemon.Running && st2.TrafficManager.Name != ""
	}, externalRolloutTimeout, externalRolloutPoll,
		"telepresence status should report a healthy connection again after the manager restart")

	s.Require().Eventually(func() bool {
		var es []cli.ListEntry
		if err := tp.JSON(ctx, &es, "list", "--format", "json", "-n", ns); err != nil {
			return false
		}
		for _, e := range es {
			if e.Name == wl.Name && e.Namespace == ns {
				return len(e.InterceptInfo) == 1
			}
		}
		return false
	}, externalRolloutTimeout, externalRolloutPoll, "exactly one intercept for %s should survive the manager restart", wl.Name)

	check.EventuallyHTTP(t, wl.ServiceURL(), check.BodyContains(ls.Marker()), externalRolloutTimeout)

	_, stderr, err = tp.Run(ctx, "quit", "-s")
	s.Require().NoError(err, "quit -s: %s", stderr)
}

// zipHasManagerLog reports whether the gather-logs zip at path carries a
// traffic-manager pod log entry.
func zipHasManagerLog(t *testing.T, path string) bool {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "traffic-manager-") && strings.HasSuffix(f.Name, ".log") {
			return true
		}
	}
	return false
}

// ExternalEndpointAnonymous proves the external listener's contract for an
// unauthenticated caller: Version is reachable, an internal-only method
// reports Unimplemented, and an ordinary client method is refused.
type ExternalEndpointAnonymous struct {
	rt.Suite
}

func init() {
	rt.Register(&ExternalEndpointAnonymous{}, rt.InArea("auth"), rt.NeedsManager(managers.Default))
}

func (s *ExternalEndpointAnonymous) Test_ExternalEndpointAnonymousProbe() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()

	nodeIP, nodePort, caPath := setupExternalEndpoint(t, ctx, r)

	caPEM, err := os.ReadFile(caPath)
	s.Require().NoError(err)
	pool := x509.NewCertPool()
	s.Require().True(pool.AppendCertsFromPEM(caPEM), "CA pem should parse")

	target := net.JoinHostPort(nodeIP, strconv.Itoa(nodePort))
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := grpcClient.DialGRPC(dialCtx, target,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: pool})))
	s.Require().NoError(err, "dialing the external endpoint anonymously")
	defer conn.Close()
	mc := manager.NewManagerClient(conn)

	// (a) Version is the deliberately public pre-session surface.
	_, err = mc.Version(ctx, &empty.Empty{})
	s.Require().NoError(err, "Version should be reachable without credentials")

	// (b) an internal-only method is absent from this listener: skipAuth
	// exempts WatchQuicBackends from the auth interceptor entirely (the
	// quic-forwarder calls it with no cluster credentials), so the request
	// reaches externalService's handler, which reports Unimplemented rather
	// than Unauthenticated.
	qbStream, err := mc.WatchQuicBackends(ctx, &empty.Empty{})
	s.Require().NoError(err)
	_, err = qbStream.Recv()
	s.Require().Error(err)
	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.Unimplemented, st.Code())

	// (c) an ordinary client method with no credentials at all is refused by
	// the external listener's own auth interceptor before it ever reaches
	// the handler.
	_, err = mc.GetClientConfig(ctx, &empty.Empty{})
	s.Require().Error(err)
	st, ok = status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.Unauthenticated, st.Code())
}
