package daemon

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/usg"
)

// startUsageSender starts a background goroutine that drains the shared
// on-disk FIFO populated by CLI invocations and by daemon-side reporters
// (e.g. session.end). The user daemon is the long-lived process; only it
// runs the sender. The producer was already installed on the daemon's main
// context by internalRun — see usg.InstallClient — so this function only
// receives the sink it created. A nil sink means usage is disabled or the
// queue could not be opened; either way there is nothing to send.
func startUsageSender(g log.Group, cfg client.Config, sink usg.Sink) {
	u := cfg.Usage()
	if u == nil || !u.Enabled || sink == nil {
		return
	}
	g.Go("usage-sender", func(ctx context.Context) error {
		usg.RunSender(ctx, sink, usg.SenderConfig{
			Address:  u.CollectorAddress,
			Insecure: u.Insecure,
		})
		return nil
	})
}

// reportSessionEnd enqueues a "session.end" usage report carrying the session
// duration and the telemetry counters from the root daemon. Called from the
// user daemon's cancelSession path. Best-effort: a nil session, a session
// that never received terminal metrics, or disabled usage reporting all
// silently no-op via usg.New / nil-Report semantics.
func reportSessionEnd(ctx context.Context, session userd.Session) {
	if session == nil {
		return
	}
	metrics := session.RootSessionEndMetrics()
	if metrics == nil {
		// The root daemon either ran in-process or never sent a terminal
		// Activity (e.g. a very short-lived session that lost its
		// activity stream). Nothing to report.
		return
	}
	usg.New(ctx, "session.end").
		AddInt("session.duration_seconds", int(metrics.GetSessionDuration().AsDuration().Seconds())).
		AddInt("rootd.outbound_tunnels", int(metrics.GetOutboundTunnels())).
		AddInt("rootd.outbound_tunnel_errors", int(metrics.GetOutboundTunnelErrors())).
		AddInt("rootd.incoming_dials", int(metrics.GetIncomingDials())).
		AddInt("rootd.incoming_dial_errors", int(metrics.GetIncomingDialErrors())).
		Send()
}
