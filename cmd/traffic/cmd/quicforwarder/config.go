// Package quicforwarder implements the "quic-forwarder" subcommand of the tel2 image:
// the stateless QUIC packet router described in "The forwarder" section of
// docs/plans/quic-transport/design.md. It owns the single UDP entry point into the
// cluster for QUIC tunnel traffic and routes every datagram, by SNI on a connection's
// first packet and by server-issued connection ID afterwards, to the traffic-manager or
// a traffic-agent -- without ever terminating TLS or holding any connection state that
// matters across a restart.
package quicforwarder

import (
	"log/slog"
	"net"
	"reflect"
	"strconv"
	"strings"

	"github.com/caarlos0/env/v11"

	"github.com/telepresenceio/clog"
)

// Env is the quic-forwarder's environment. Every field has a default so the command is
// runnable (against an unreachable manager, which just means the allowlist never
// becomes ready) with no configuration at all; a Helm chart task wires the manager
// Service DNS name and the two ports into the Deployment.
type Env struct {
	// ListenPort is the UDP port the forwarder's single client-facing socket
	// binds to on all interfaces. Matches the chart's quicTunnel.port default
	// (values.yaml), since today that port is the manager's own QUIC listener;
	// the forwarder takes over binding it once it sits in front of the manager.
	ListenPort uint16 `default:"7778"`

	// BackendPort is the UDP port every backend (the traffic-manager, and every
	// traffic-agent once phase 6 lands) binds its own QUIC listener to. It is a
	// single cluster-wide constant, not per-backend configuration: the
	// allowlist tells the forwarder which pod IPs are live backends, and this
	// port is where all of them listen.
	BackendPort uint16 `default:"7778"`

	// ManagerHost is the traffic-manager's in-cluster DNS name (or IP), used to
	// dial its gRPC WatchQuicBackends endpoint for the backend allowlist. The
	// chart's Service name for the manager is "traffic-manager" in the
	// manager's own namespace; when the forwarder runs in that same namespace
	// (the expected topology) the unqualified name resolves correctly.
	ManagerHost string `default:"traffic-manager"`

	// ManagerPort is the traffic-manager's plaintext gRPC port (chart value
	// apiPort, default 8081) -- the same port the traffic-agent's
	// TalkToManager dials.
	ManagerPort uint16 `default:"8081"`

	// LogLevel controls this process's log verbosity.
	LogLevel slog.Level `default:"info"`
}

// ManagerAddress returns the host:port the forwarder dials to reach the
// traffic-manager's gRPC endpoint.
func (e *Env) ManagerAddress() string {
	return net.JoinHostPort(e.ManagerHost, strconv.Itoa(int(e.ManagerPort)))
}

// LoadEnv parses the quic-forwarder's Env from envMap, or from the process environment
// when envMap is nil (the same convention managerutil.LoadEnv uses, so tests can inject
// an explicit map while production code just calls LoadEnv(nil)).
func LoadEnv(envMap map[string]string) (*Env, error) {
	e := &Env{}
	err := env.ParseWithOptions(e, env.Options{
		Environment:           envMap,
		DefaultValueTagName:   "default",
		UseFieldNameByDefault: true,
		FuncMap: map[reflect.Type]env.ParserFunc{
			reflect.TypeOf(slog.Level(0)): func(s string) (any, error) {
				if strings.EqualFold(s, "warning") {
					s = "warn"
				}
				return clog.ParseLevel(s)
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return e, nil
}
