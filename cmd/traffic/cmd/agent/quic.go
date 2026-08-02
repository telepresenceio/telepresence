package agent

import (
	"context"
	"strconv"
	"sync"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agent/quicserver"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/sessiontoken"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// quicAgentState guards the agent's QUIC listener across manager reconnects. It is
// created once, in NewState, and lives for the lifetime of the agent process; the
// *quicserver.Listener it holds is created lazily, the first time
// RefreshQuicAgentListener sees a manager with QUIC enabled.
type quicAgentState struct {
	mu       sync.Mutex
	listener *quicserver.Listener
}

// RefreshQuicAgentListener implements the State method of the same name and serves two
// purposes on every (re-)established manager session, regardless of whether this pod
// runs a QUIC listener: it keeps s.fileShareAuth's session-credential material current
// for the FTP and SFTP listeners (see "Traffic-agent ports" in
// docs/reference/authentication.md), and, only when AGENT_QUIC_PORT is set, it also
// starts or refreshes the QUIC listener itself. See "Agent connections over QUIC" in
// docs/reference/quic-transport-architecture.md.
func (s *state) RefreshQuicAgentListener(processCtx, fetchCtx context.Context) {
	resp, err := s.manager.GetQuicAgentCert(fetchCtx, s.sessionInfo)
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			// The traffic-manager predates agent QUIC certificates. Expected skew;
			// the agent stays on its port-forwarded path, and fileShareAuth stays
			// unpopulated, so file-sharing connections keep being accepted
			// unauthenticated.
			clog.Debugf(fetchCtx, "quicserver: traffic-manager predates QUIC agent certificates")
			return
		}
		clog.Errorf(fetchCtx, "quicserver: failed to fetch QUIC agent certificate: %v", err)
		return
	}
	if !resp.GetEnabled() {
		// The manager we're now talking to has no QUIC CA configured, so it has no
		// session credential to hand out either; fileShareAuth stays unpopulated. If a
		// listener from an earlier, QUIC-enabled manager is still running, it keeps
		// running on its old material: connections from a client minted by this new
		// manager will simply fail certificate verification, which is the correct
		// fallback (the client falls back to its port-forward path). Nothing to do
		// here but say so.
		clog.Debugf(fetchCtx, "quicserver: traffic-manager has no QUIC CA configured; not starting a QUIC listener")
		return
	}

	material, err := quicserver.ParseMaterial(resp.GetCertPem(), resp.GetKeyPem(), resp.GetCaPem())
	if err != nil {
		clog.Errorf(fetchCtx, "quicserver: failed to parse QUIC agent certificate: %v", err)
		return
	}
	pub, err := sessiontoken.PublicKeyFromCertPEM(resp.GetCaPem())
	if err != nil {
		clog.Errorf(fetchCtx, "quicserver: failed to parse session-token public key: %v", err)
		return
	}
	s.fileShareAuth.set(pub, resp.GetAuthenticationMode())

	quicPort, ok := quicPortFromEnv(fetchCtx)
	if !ok {
		return
	}

	qa := s.quicAgent
	qa.mu.Lock()
	defer qa.mu.Unlock()
	if qa.listener != nil {
		qa.listener.SetMaterial(material)
		clog.Debugf(fetchCtx, "quicserver: refreshed QUIC listener TLS material for %s", resp.GetSni())
		return
	}

	ln, err := quicserver.New(s.PodIP(), quicPort, s.grpcServer, material)
	if err != nil {
		clog.Errorf(fetchCtx, "quicserver: failed to start QUIC listener on port %d: %v", quicPort, err)
		return
	}
	qa.listener = ln
	clog.Infof(fetchCtx, "quicserver: QUIC listener started on %s for %s", ln.Addr(), resp.GetSni())
	go func() {
		if err := ln.Serve(processCtx); err != nil {
			clog.Errorf(processCtx, "quicserver: QUIC listener ended with error: %v", err)
		}
	}()
}

// quicPortFromEnv reads AGENT_QUIC_PORT, returning ok == false when it is unset,
// empty, zero, or invalid -- all of which mean "this pod has no QUIC listener to
// run," never an error worth logging (an older or QUIC-less traffic-manager simply
// never sets the variable).
func quicPortFromEnv(ctx context.Context) (uint16, bool) {
	v, ok := dos.LookupEnv(ctx, agentconfig.EnvAgentQuicPort)
	if !ok || v == "" {
		return 0, false
	}
	port, err := strconv.ParseUint(v, 10, 16)
	if err != nil || port == 0 {
		return 0, false
	}
	return uint16(port), true
}
