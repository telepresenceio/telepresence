package auth

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	grpcClient "github.com/telepresenceio/telepresence/v2/pkg/grpc/client"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/sessiontoken"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AgentidSessionAccess drives a session that holds only the connect grant
// -- no attachment grant anywhere, and no namespace grant on the victim's
// namespace -- straight at a victim traffic-agent's gRPC Tunnel port, under
// enforcing mode. The session credential it presents is the one
// GetSessionCredential mints for its own session; the only question under
// test is whether the agent accepts that credential for dials and file
// access it was never granted.
type AgentidSessionAccess struct {
	rt.Suite
}

func init() {
	rt.Register(&AgentidSessionAccess{}, rt.InArea("auth"), rt.NeedsManager(managers.AuthEnforcing()))
}

// agentidRestConfig builds a *rest.Config from the run's RTEST_KUBECONFIG/
// RTEST_CONTEXT (falling back to KUBECONFIG), the same inputs the CLI and
// rt.ManagerClient use, so a direct pod dial targets the same cluster and
// context as the rest of the run.
func agentidRestConfig() (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	path := os.Getenv("RTEST_KUBECONFIG")
	if path == "" {
		path = os.Getenv("KUBECONFIG")
	}
	if path != "" {
		rules.ExplicitPath = path
	}
	overrides := &clientcmd.ConfigOverrides{}
	if kubeCtx := os.Getenv("RTEST_CONTEXT"); kubeCtx != "" {
		overrides.CurrentContext = kubeCtx
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
}

// agentidDialAgentPod dials podName/podUID in ns on port directly (not
// through the traffic-manager) over a port-forward built from the run's own
// kubeconfig, mirroring rt.ManagerClient's transport but targeting an
// arbitrary pod. This stands in for the network path a connected client
// already has to any agent (its own dial-watcher/tunnel plumbing, set up by
// the session, not a fresh per-agent Kubernetes RBAC grant); the port-forward
// here is test-process infrastructure, not part of what is under test.
func agentidDialAgentPod(ctx context.Context, ns, podName, podUID string, port int32) (agent.AgentClient, func(), error) {
	cfg, err := agentidRestConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("rtest: building rest.Config: %w", err)
	}
	k8sAPI, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("rtest: building kubernetes client: %w", err)
	}
	ctx = client.WithConfig(ctx, client.GetDefaultConfig())
	ctx = k8sapi.WithK8sInterface(ctx, k8sAPI)
	ctx = portforward.WithRestConfig(ctx, cfg)
	pa := &portforward.PodAddress{Name: podName, Namespace: ns, Port: uint16(port), PodID: k8sTypes.UID(podUID)}
	target := portforward.K8sPFScheme + ":///" + pa.String()
	conn, err := grpcClient.DialGRPC(ctx, target,
		grpc.WithResolvers(portforward.NewResolver(ctx)),
		grpc.WithContextDialer(portforward.Dialer(ctx)),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("rtest: dialing pod %s.%s: %w", podName, ns, err)
	}
	return agent.NewAgentClient(conn), func() { _ = conn.Close() }, nil
}

// agentidAgentInfoFor opens a session under the framework's broadly
// granted test identity and reads back the AgentInfo WatchAgents reports
// for workloadName in ns: pod name/UID and the agent's gRPC/SFTP/FTP ports.
// This is the admin-level step that discovers where the victim agent lives;
// it is not part of the attack under test.
func agentidAgentInfoFor(t *testing.T, ctx context.Context, r *rt.Runtime, ns, workloadName string) *manager.AgentInfo {
	t.Helper()
	tok := kubectlCreateToken(t, ctx, r, managers.TestServiceAccount)
	mc, tokCtx, si := dialAndArrive(t, ctx, r, ns, "rtest-agentid-watch-"+workloadName, tok)

	watchCtx, cancel := context.WithTimeout(tokCtx, 15*time.Second)
	defer cancel()
	stream, err := mc.WatchAgents(watchCtx, si)
	if err != nil {
		t.Fatalf("WatchAgents: %v", err)
	}
	for {
		snap, err := stream.Recv()
		if err != nil {
			t.Fatalf("WatchAgents: waiting for %s's AgentInfo: %v", workloadName, err)
		}
		for _, a := range snap.GetAgents() {
			if a.GetName() == workloadName {
				return a
			}
		}
	}
}

// agentidSessionCredential opens a session for a ServiceAccount holding
// only the connect grant and returns the session token GetSessionCredential
// mints for it, the way a connected client itself fetches one
// (pkg/client/sessioncred).
func agentidSessionCredential(t *testing.T, ctx context.Context, r *rt.Runtime, ns, name string) string {
	t.Helper()
	createGrantIdentity(t, ctx, r, name, minimalRBACRules)
	tok := kubectlCreateToken(t, ctx, r, name)
	mc, tokCtx, si := dialAndArrive(t, ctx, r, ns, name, tok)

	cred, err := mc.GetSessionCredential(tokCtx, si)
	if err != nil {
		t.Fatalf("GetSessionCredential: %v", err)
	}
	return cred.GetToken()
}

// agentidOpenAgentTunnel opens a Tunnel call to ac, authenticated with
// sessionToken, declaring sessionID as the caller's own session (the one
// the token actually names) and dst as the connection target, and returns
// it wrapped as a net.Conn once the StreamInfo/StreamOK handshake
// completes. Every read and write on the returned conn is bounded by ctx.
func agentidOpenAgentTunnel(ctx context.Context, ac agent.AgentClient, sessionToken, sessionID string, dst netip.AddrPort) (net.Conn, error) {
	tokCtx := metadata.AppendToOutgoingContext(ctx, sessiontoken.MetadataKey, sessionToken)
	grpcStream, err := ac.Tunnel(tokCtx)
	if err != nil {
		return nil, err
	}
	id := tunnel.NewConnID(types.ProtoTCP, netip.MustParseAddrPort("127.0.0.1:1"), dst)
	s, err := tunnel.NewClientStream(ctx, tunnel.ClientToAgent, grpcStream, id, tunnel.SessionID(sessionID), 0, 5*time.Second)
	if err != nil {
		return nil, err
	}
	return tunnel.NewStreamConn(ctx, s, nil, nil), nil
}

// readLineWithin reads one '\n'-terminated line from r, or returns an error
// once ctx is done. r's own reads are not individually cancelable, so this
// only bounds how long the caller waits for the attempt to resolve: on a
// Tunnel that never receives data (a dial rejected deep inside the stream,
// which the test's own ctx governs) it still returns once ctx expires.
func readLineWithin(ctx context.Context, r *bufio.Reader) (string, error) {
	type res struct {
		line string
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		line, err := r.ReadString('\n')
		ch <- res{line, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case v := <-ch:
		return v.line, v.err
	}
}

// agentidRefused reports whether err is how the agent turns a caller away: a
// PermissionDenied or Unauthenticated status, or the connection being closed
// on the caller. Anything else (a timeout, a missing file, a transport
// failure) says the attempt did not get as far as the agent's decision.
func agentidRefused(err error) bool {
	if err == nil {
		return false
	}
	if sc, ok := status.FromError(err); ok {
		switch sc.Code() {
		case codes.PermissionDenied, codes.Unauthenticated:
			return true
		default:
		}
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed)
}

// agentidRequireRefusedOrEnded fails the test unless err is a refusal by the
// agent; what is being attempted is named by step.
func agentidRequireRefusedOrEnded(t *testing.T, step string, err error) {
	t.Helper()
	if !agentidRefused(err) {
		t.Fatalf("%s failed without the agent refusing it (the attempt did not get that far): %v", step, err)
	}
}

// Test_TunnelToLoopbackFtpBannerRefused opens a Tunnel through the victim's
// traffic-agent to its own loopback FTP port, authenticated with a session
// credential for an identity that holds only the connect grant (no
// attachment grant on the victim, and no grant at all in its namespace).
// The FTP server announces itself with an unsolicited banner line before
// any login, so receiving one needs nothing but a working dial: this
// session must never get one.
func (s *AgentidSessionAccess) Test_TunnelToLoopbackFtpBannerRefused() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	victim := s.Workload(workloads.EchoWithConfigVolume("agentid-a1-ftp"))
	_, _ = agentidInstallFileShareAgent(s, victim)
	info := agentidAgentInfoFor(t, ctx, r, ns, victim.Name)
	s.Require().NotZero(info.GetFtpPort(), "%s should have a traffic-agent with an FTP port", victim.Name)

	ac, closeFn, err := agentidDialAgentPod(ctx, ns, info.GetPodName(), info.GetPodUid(), info.GetApiPort())
	s.Require().NoError(err)
	defer closeFn()

	sessionTok := agentidSessionCredential(t, ctx, r, ns, "rtest-agentid-ftp-banner")
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	dst := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(info.GetFtpPort()))
	conn, err := agentidOpenAgentTunnel(callCtx, ac, sessionTok, sessionTokenSessionID(t, sessionTok), dst)
	if err != nil {
		agentidRequireRefusedOrEnded(t, "opening the tunnel to the FTP port", err)
		return
	}
	defer conn.Close()
	line, rerr := readLineWithin(callCtx, bufio.NewReader(conn))
	if rerr != nil {
		agentidRequireRefusedOrEnded(t, "reading the FTP banner", rerr)
		return
	}
	gotBanner := strings.HasPrefix(line, "220")
	s.False(gotBanner, "a session without access to this agent must not reach its loopback FTP port through the tunnel")
}

// Test_SftpMarkerFileReadRefused opens a Tunnel through the victim's
// traffic-agent to its own loopback SFTP port, with the same
// connect-grant-only session credential, and attempts to read the file the
// victim's ConfigMap mount exposes. The exact marker content must never
// come back.
func (s *AgentidSessionAccess) Test_SftpMarkerFileReadRefused() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	victim := s.Workload(workloads.EchoWithConfigVolume("agentid-a1-sftp"))
	container, mountedPath := agentidInstallFileShareAgent(s, victim)
	info := agentidAgentInfoFor(t, ctx, r, ns, victim.Name)
	ci := info.GetContainers()[container]
	s.Require().NotNil(ci, "%s's AgentInfo has no entry for container %q", victim.Name, container)
	remotePath := ci.GetMountPoint() + mountedPath
	s.Require().NotZero(info.GetSftpPort(), "%s should have a traffic-agent with an SFTP port", victim.Name)

	ac, closeFn, err := agentidDialAgentPod(ctx, ns, info.GetPodName(), info.GetPodUid(), info.GetApiPort())
	s.Require().NoError(err)
	defer closeFn()

	sessionTok := agentidSessionCredential(t, ctx, r, ns, "rtest-agentid-sftp-read")
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	dst := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(info.GetSftpPort()))
	conn, err := agentidOpenAgentTunnel(callCtx, ac, sessionTok, sessionTokenSessionID(t, sessionTok), dst)
	if err != nil {
		agentidRequireRefusedOrEnded(t, "opening the tunnel to the SFTP port", err)
		return
	}
	defer conn.Close()
	sc, cerr := sftp.NewClientPipe(conn, conn)
	if cerr != nil {
		agentidRequireRefusedOrEnded(t, "starting the SFTP session", cerr)
		return
	}
	defer sc.Close()
	f, ferr := sc.Open(remotePath)
	if ferr != nil {
		agentidRequireRefusedOrEnded(t, "opening "+remotePath, ferr)
		return
	}
	defer f.Close()
	data, rerr := io.ReadAll(f)
	if rerr != nil {
		agentidRequireRefusedOrEnded(t, "reading "+remotePath, rerr)
		return
	}
	gotContent := string(data) == workloads.ConfigVolumeContent
	if !gotContent {
		t.Fatalf("read %q from %s, which is neither the marker content nor a refusal", data, remotePath)
	}
	s.False(gotContent, "a session without access to this agent must not read its mounted files through the SFTP port")
}

// Test_FtpLoginWithSessionTokenRefused opens a Tunnel to the victim's
// loopback FTP port and logs in with the connect-grant-only session
// credential as the password, exactly as a legitimate client's go-fuseftp
// does. The login must not succeed.
func (s *AgentidSessionAccess) Test_FtpLoginWithSessionTokenRefused() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	victim := s.Workload(workloads.EchoWithConfigVolume("agentid-a1-ftplogin"))
	_, _ = agentidInstallFileShareAgent(s, victim)
	info := agentidAgentInfoFor(t, ctx, r, ns, victim.Name)
	s.Require().NotZero(info.GetFtpPort(), "%s should have a traffic-agent with an FTP port", victim.Name)

	ac, closeFn, err := agentidDialAgentPod(ctx, ns, info.GetPodName(), info.GetPodUid(), info.GetApiPort())
	s.Require().NoError(err)
	defer closeFn()

	sessionTok := agentidSessionCredential(t, ctx, r, ns, "rtest-agentid-ftp-login")
	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	dst := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(info.GetFtpPort()))
	conn, err := agentidOpenAgentTunnel(callCtx, ac, sessionTok, sessionTokenSessionID(t, sessionTok), dst)
	if err != nil {
		agentidRequireRefusedOrEnded(t, "opening the tunnel to the FTP port", err)
		return
	}
	defer conn.Close()
	rd := bufio.NewReader(conn)
	if _, berr := readLineWithin(callCtx, rd); berr != nil {
		agentidRequireRefusedOrEnded(t, "reading the FTP banner", berr)
		return
	}
	fmt.Fprintf(conn, "USER anonymous\r\n")
	if _, uerr := readLineWithin(callCtx, rd); uerr != nil {
		agentidRequireRefusedOrEnded(t, "answering USER", uerr)
		return
	}
	fmt.Fprintf(conn, "PASS %s\r\n", sessionTok)
	resp, perr := readLineWithin(callCtx, rd)
	if perr != nil {
		agentidRequireRefusedOrEnded(t, "answering PASS", perr)
		return
	}
	loggedIn := strings.HasPrefix(resp, "230")
	s.False(loggedIn, "a session without access to this agent must not log in to its FTP port using its own session token")
}

// agentidInstallFileShareAgent connects as the suite's granted identity,
// intercepts wl with mounts enabled so the agent reports its mount paths,
// and returns the intercepted container and the remote path of EchoWithConfigVolume's marker file (the
// path the client itself is told, TELEPRESENCE_MOUNTS, rather than an
// assumption about the agent's internal layout). The agent is left running
// after detach.
func agentidInstallFileShareAgent(s *AgentidSessionAccess, wl *rt.Workload) (container, path string) {
	t := s.T()
	conn := s.Connect()
	a := conn.Intercept(t, wl, cli.MountFalse())
	remotePaths := strings.Split(a.Intercept.Environment[agentconfig.EnvInterceptMounts], ":")
	container = a.Intercept.Environment[agentconfig.EnvInterceptContainer]
	a.Detach(t)
	freeDefaultConnection(t, s.AppNamespace())

	for _, p := range remotePaths {
		if strings.HasSuffix(p, workloads.ConfigVolumeMountPath) {
			return container, p + "/" + workloads.ConfigVolumeFileName
		}
	}
	t.Fatalf("%s: no mounted path ends in %s (TELEPRESENCE_MOUNTS=%q)", wl.Name, workloads.ConfigVolumeMountPath, remotePaths)
	return "", ""
}

// sessionTokenSessionID extracts the session ID a session token names,
// without a signing key: both the attacker and a legitimate verifier agree
// on the wire format (pkg/sessiontoken), and declaring anything but the
// token's own session ID would make verifySession reject the call for an
// unrelated reason (a foreign declared session), not the one under test.
func sessionTokenSessionID(t *testing.T, tok string) string {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 4 {
		t.Fatalf("session token %q does not have the expected v1 shape", tok)
	}
	return parts[1]
}
