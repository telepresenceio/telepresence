package auth

import (
	"context"
	"io"
	"net/netip"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// agentidUnimplementedLog is the line a traffic-agent logs when its
// traffic-manager answers AuthorizeSessionAccess with Unimplemented.
const agentidUnimplementedLog = "doesn't implement AuthorizeSessionAccess"

// agentidQuicCertUnimplementedLog is the line a traffic-agent logs when its
// traffic-manager answers GetQuicAgentCert with Unimplemented.
const agentidQuicCertUnimplementedLog = "doesn't implement GetQuicAgentCert"

// AgentidPreAccessCallManager runs a current traffic-agent against an
// enforcing traffic-manager that answers Unimplemented for
// AuthorizeSessionAccess, as a manager older than 2.34 does. A 2.34
// traffic-agent requires a 2.34 traffic-manager, so it refuses every session
// instead of trusting the session credential alone; the suite proves that a
// client holding every grant gets no file access.
type AgentidPreAccessCallManager struct {
	rt.Suite
}

func init() {
	rt.Register(&AgentidPreAccessCallManager{}, rt.InArea("auth"), rt.NeedsManager(managers.AgentidEnforcingPreAccessCall()),
		rt.Requires(rt.FUSE), rt.NotOn("windows"))
}

// Test_OldManagerRefusesFileAccess opens a Tunnel through the traffic-agent
// to its own loopback SFTP port, using the session credential of the
// framework's broadly granted identity, and tries to read the file that the
// workload's ConfigMap mount exposes. The agent must refuse the tunnel (or
// end the connection) and the marker content must never come back. The
// agent's log must also say that the traffic-manager doesn't implement
// AuthorizeSessionAccess, so the refusal cannot have another cause.
func (s *AgentidPreAccessCallManager) Test_OldManagerRefusesFileAccess() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	wl := s.Workload(workloads.EchoWithConfigVolume("agentid-compat-old-manager"))
	container, mountedPath := agentidInstallFileShareAgent(s, wl)
	info := agentidAgentInfoFor(t, ctx, r, ns, wl.Name)
	ci := info.GetContainers()[container]
	s.Require().NotNil(ci, "%s's AgentInfo has no entry for container %q", wl.Name, container)
	remotePath := ci.GetMountPoint() + mountedPath
	s.Require().NotZero(info.GetSftpPort(), "%s should have a traffic-agent with an SFTP port", wl.Name)

	ac, closeFn, err := agentidDialAgentPod(ctx, ns, info.GetPodName(), info.GetPodUid(), info.GetApiPort())
	s.Require().NoError(err)
	defer closeFn()

	tok := kubectlCreateToken(t, ctx, r, managers.TestServiceAccount)
	mc, tokCtx, si := dialAndArrive(t, ctx, r, ns, "rtest-agentid-old-manager", tok)
	cred, err := mc.GetSessionCredential(tokCtx, si)
	s.Require().NoError(err)

	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	dst := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(info.GetSftpPort()))
	conn, err := agentidOpenAgentTunnel(callCtx, ac, cred.GetToken(), sessionTokenSessionID(t, cred.GetToken()), dst)
	if err == nil {
		defer conn.Close()
		err = agentidReadRemoteFile(conn, remotePath)
	}
	s.Require().Error(err, "a traffic-agent whose traffic-manager lacks AuthorizeSessionAccess must not serve %s", remotePath)
	agentidRequireRefusedOrEnded(t, "reading "+remotePath, err)

	s.Require().Eventually(func() bool {
		log, err := agentidAgentLogs(ctx, r, ns, wl.Name)
		return err == nil && strings.Contains(log, agentidUnimplementedLog)
	}, agentidMountTimeout, time.Second, "the traffic-agent of %s should log that the traffic-manager doesn't implement AuthorizeSessionAccess", wl.Name)
}

// AgentidPreQuicCertManager runs a current traffic-agent against a
// traffic-manager that answers Unimplemented for GetQuicAgentCert, as a
// manager older than 2.31 does. The agent requires a newer traffic-manager,
// so it must refuse tunnel calls instead of serving them unauthenticated.
type AgentidPreQuicCertManager struct {
	rt.Suite
}

func init() {
	rt.Register(&AgentidPreQuicCertManager{}, rt.InArea("auth"), rt.NeedsManager(managers.AgentidEnforcingPreQuicAgentCert()),
		rt.Requires(rt.FUSE), rt.NotOn("windows"))
}

// Test_OldManagerRefusesTunnel opens a Tunnel through the traffic-agent to
// its own loopback SFTP port, as a client session of the old traffic-manager
// (which issues no session credential), and tries to read the file that the
// workload's ConfigMap mount exposes. The agent must refuse the call and the
// marker content must never come back. The agent's log must say that the
// traffic-manager doesn't implement GetQuicAgentCert.
func (s *AgentidPreQuicCertManager) Test_OldManagerRefusesTunnel() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	wl := s.Workload(workloads.EchoWithConfigVolume("agentid-compat-no-cert"))
	container, mountedPath := agentidInstallFileShareAgent(s, wl)
	info := agentidAgentInfoFor(t, ctx, r, ns, wl.Name)
	ci := info.GetContainers()[container]
	s.Require().NotNil(ci, "%s's AgentInfo has no entry for container %q", wl.Name, container)
	remotePath := ci.GetMountPoint() + mountedPath
	s.Require().NotZero(info.GetSftpPort(), "%s should have a traffic-agent with an SFTP port", wl.Name)

	ac, closeFn, err := agentidDialAgentPod(ctx, ns, info.GetPodName(), info.GetPodUid(), info.GetApiPort())
	s.Require().NoError(err)
	defer closeFn()

	tok := kubectlCreateToken(t, ctx, r, managers.TestServiceAccount)
	_, _, si := dialAndArrive(t, ctx, r, ns, "rtest-agentid-no-cert", tok)

	callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	dst := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(info.GetSftpPort()))
	conn, err := agentidOpenAgentTunnel(callCtx, ac, "", si.GetSessionId(), dst)
	if err == nil {
		defer conn.Close()
		err = agentidReadRemoteFile(conn, remotePath)
	}
	s.Require().Error(err, "a traffic-agent whose traffic-manager lacks GetQuicAgentCert must not serve %s", remotePath)
	if status.Code(err) != codes.Unavailable {
		agentidRequireRefusedOrEnded(t, "reading "+remotePath, err)
	}

	s.Require().Eventually(func() bool {
		log, err := agentidAgentLogs(ctx, r, ns, wl.Name)
		return err == nil && strings.Contains(log, agentidQuicCertUnimplementedLog)
	}, agentidMountTimeout, time.Second, "the traffic-agent of %s should log that the traffic-manager doesn't implement GetQuicAgentCert", wl.Name)
}

// agentidReadRemoteFile reads path over an SFTP session on conn. It returns
// nil only when the marker content came back; any other content is an error
// that is not a refusal.
func agentidReadRemoteFile(conn io.ReadWriteCloser, path string) error {
	sc, err := sftp.NewClientPipe(conn, conn)
	if err != nil {
		return err
	}
	defer sc.Close()
	f, err := sc.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if string(data) != workloads.ConfigVolumeContent {
		return io.ErrUnexpectedEOF
	}
	return nil
}
