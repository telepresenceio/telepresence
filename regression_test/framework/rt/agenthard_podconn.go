package rt

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/client-go/kubernetes"

	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// AgenthardPodDialer returns a function that opens a TCP connection to a port of a pod
// through the Kubernetes API server's port-forward, using the run's kubeconfig and context.
// The connection reaches the port from inside the pod's network namespace and needs neither
// a traffic-manager nor a telepresence connection.
func AgenthardPodDialer(e Env) (func(ctx context.Context, ns, pod string, port uint16) (net.Conn, error), error) {
	cfg, err := managerRestConfig(e.R)
	if err != nil {
		return nil, fmt.Errorf("rtest: building rest.Config: %w", err)
	}
	k8sAPI, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("rtest: building kubernetes client: %w", err)
	}
	return func(ctx context.Context, ns, pod string, port uint16) (net.Conn, error) {
		pctx := client.WithConfig(e.Ctx, client.GetDefaultConfig())
		pctx = k8sapi.WithK8sInterface(pctx, k8sAPI)
		pctx = portforward.WithRestConfig(pctx, cfg)
		addr := fmt.Sprintf("pod/%s.%s:%d%s%s", pod, ns, port, portforward.UIDSeparator, portforward.NoLookupMarker)
		return portforward.Dialer(pctx)(ctx, addr)
	}, nil
}

var agentGRPCListenerRE = regexp.MustCompile(`Listener opened on \S*:(\d+)`)

// AgenthardSftpDialer returns a function that opens a connection to the SFTP server of the
// traffic-agent (sidecar) or traffic-node-agent in a pod the way a client does: a Tunnel call on the agent's gRPC port, with
// the agent's loopback SFTP port as destination. The agent must run in permissive mode, since
// the call carries no session credential. Closing the connection ends the call.
func AgenthardSftpDialer(e Env) (func(ctx context.Context, ns, pod string, sftpPort uint16) (net.Conn, error), error) {
	dial, err := AgenthardPodDialer(e)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, ns, pod string, sftpPort uint16) (net.Conn, error) {
		names, err := e.R.Kubectl(ctx, ns, "get", "pod", pod, "-o", `jsonpath={range .spec.containers[*]}{.name}{"\n"}{end}`)
		if err != nil {
			return nil, fmt.Errorf("reading the containers of %s.%s: %w", pod, ns, err)
		}
		container := ""
		for _, n := range strings.Fields(names) {
			if n == "traffic-agent" || n == "traffic-node-agent" {
				container = n
			}
		}
		if container == "" {
			return nil, fmt.Errorf("pod %s.%s has no traffic-agent or traffic-node-agent container (containers: %q)", pod, ns, names)
		}
		logs, err := e.R.Kubectl(ctx, ns, "logs", pod, "-c", container)
		if err != nil {
			return nil, fmt.Errorf("reading the logs of the traffic-agent in %s.%s: %w", pod, ns, err)
		}
		m := agentGRPCListenerRE.FindStringSubmatch(logs)
		if m == nil {
			return nil, fmt.Errorf("the log of the traffic-agent in %s.%s has no gRPC listener line", pod, ns)
		}
		grpcPort, err := strconv.ParseUint(m[1], 10, 16)
		if err != nil {
			return nil, err
		}
		cc, err := grpc.NewClient("passthrough:///agenthard-agent",
			grpc.WithContextDialer(func(dctx context.Context, _ string) (net.Conn, error) {
				return dial(dctx, ns, pod, uint16(grpcPort))
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, err
		}
		sctx, cancel := context.WithCancel(e.Ctx)
		fail := func(err error) (net.Conn, error) {
			cancel()
			_ = cc.Close()
			return nil, err
		}
		grpcStream, err := agent.NewAgentClient(cc).Tunnel(sctx)
		if err != nil {
			return fail(err)
		}
		const sessionID = "agenthard-sftp"
		id := tunnel.NewConnID(types.ProtoTCP, netip.MustParseAddrPort("127.0.0.1:1"),
			netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), sftpPort))
		s, err := tunnel.NewClientStream(sctx, tunnel.ClientToAgent, grpcStream, id, sessionID, 0, 5*time.Second)
		if err != nil {
			return fail(err)
		}
		return &tunnelConn{Conn: tunnel.NewStreamConn(sctx, s, nil, nil), cancel: cancel, cc: cc}, nil
	}, nil
}

type tunnelConn struct {
	net.Conn
	cancel context.CancelFunc
	cc     *grpc.ClientConn
}

func (c *tunnelConn) Close() error {
	err := c.Conn.Close()
	c.cancel()
	_ = c.cc.Close()
	return err
}
