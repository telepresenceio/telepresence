package portforward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/puzpuzpuz/xsync/v3"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

const ProtocolV1Name = "portforward.k8s.io"

// PodDialer can dial a pod to establish a PodConnection.
type PodDialer interface {
	Connect(cacheDelete func()) (PodConnection, error)
}

// PodConnection represents a port agnostic stream connection to a pod. This connection can
// then be used when creating a port-specific connection to the connected pod.
type PodConnection interface {
	httpstream.Connection
	Dial(ctx context.Context, remotePort uint16) (net.Conn, error)
}

type podDialer struct {
	streamDialer httpstream.Dialer
}

type podConn struct {
	httpstream.Connection
	requestID int64
	refCount  int64
	onClose   func()
}

type dialerKey struct{}

type config struct {
	cache      *xsync.MapOf[types.UID, PodConnection]
	restConfig *rest.Config
}

func WithRestConfig(ctx context.Context, restConfig *rest.Config) context.Context {
	return context.WithValue(ctx, dialerKey{}, &config{
		cache:      xsync.NewMapOf[types.UID, PodConnection](),
		restConfig: restConfig,
	})
}

func Dialer(ctx context.Context) func(ctx context.Context, address string) (net.Conn, error) {
	cfg, ok := ctx.Value(dialerKey{}).(*config)
	return func(grpcCtx context.Context, address string) (net.Conn, error) {
		if !ok {
			return nil, errors.New("grpc dialer is not configured")
		}
		return dialContext(grpcCtx, ctx, address, cfg)
	}
}

func dialContext(grpcCtx, logCtx context.Context, addr string, cfg *config) (net.Conn, error) {
	pa, err := parsePodAddr(addr)
	if err != nil {
		dlog.Error(logCtx, err)
		return nil, err
	}
	key := pa.podID
	if key == "" {
		err = errors.New("pod ID is empty")
		dlog.Error(logCtx, err)
		return nil, err
	}
	pc, _ := cfg.cache.Compute(key, func(pc PodConnection, loaded bool) (PodConnection, bool) {
		if loaded {
			return pc, false
		}
		var pd PodDialer
		pd, err = NewPodDialer(logCtx, cfg.restConfig, pa.name, pa.namespace, client.GetConfig(logCtx).Cluster().ForceSPDY)
		if err != nil {
			return nil, true
		}
		pc, err = pd.Connect(func() {
			cfg.cache.Delete(key)
		})
		if err != nil {
			return nil, true
		}
		return pc, false
	})
	if err != nil {
		return nil, err
	}
	return pc.Dial(grpcCtx, pa.port)
}

func NewPodDialer(ctx context.Context, config *rest.Config, podName, namespace string, forceSPDY bool) (PodDialer, error) {
	err := setKubernetesDefaults(config)
	if err != nil {
		return nil, err
	}
	rc, err := rest.RESTClientFor(config)
	if err != nil {
		return nil, err
	}
	url := rc.
		Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("portforward").
		URL()

	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return nil, err
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", url)
	if !forceSPDY {
		dlog.Debugf(ctx, "Using WebSocket based port-forward to pod %s.%s", podName, namespace)
		tunnelingDialer, err := portforward.NewSPDYOverWebsocketDialer(url, config)
		if err != nil {
			return nil, err
		}
		// First attempt tunneling (websocket) dialer, then fallback to spdy dialer.
		dialer = portforward.NewFallbackDialer(tunnelingDialer, dialer, func(err error) bool {
			return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
		})
	} else {
		dlog.Debugf(ctx, "Using SPDY based port-forward to pod %s.%s", podName, namespace)
	}
	return podDialer{streamDialer: dialer}, nil
}

func (pd podDialer) Connect(onClose func()) (PodConnection, error) {
	var err error
	var protocol string
	streamConn, protocol, err := pd.streamDialer.Dial(ProtocolV1Name)
	if err != nil {
		return nil, fmt.Errorf("error upgrading connection: %s", err)
	}
	if protocol != ProtocolV1Name {
		return nil, fmt.Errorf("unable to negotiate protocol: client supports %q, server returned %q", ProtocolV1Name, protocol)
	}
	return &podConn{Connection: streamConn, onClose: onClose}, nil
}

func (pc *podConn) Dial(ctx context.Context, remotePort uint16) (conn net.Conn, err error) {
	atomic.AddInt64(&pc.refCount, 1)
	var dataStream, errorStream httpstream.Stream
	defer func() {
		if err != nil {
			atomic.AddInt64(&pc.refCount, -1)
			if errorStream != nil {
				errorStream.Close()
				pc.RemoveStreams(errorStream)
			}
		}
	}()

	requestID := atomic.AddInt64(&pc.requestID, 1)
	// create error stream
	headers := http.Header{}
	headers.Set(core.StreamType, core.StreamTypeError)
	headers.Set(core.PortHeader, strconv.Itoa(int(remotePort)))
	headers.Set(core.PortForwardRequestIDHeader, strconv.Itoa(int(requestID)))
	errorStream, err = pc.CreateStream(headers)
	if err != nil {
		return nil, fmt.Errorf("error creating error stream for port %d: %v", remotePort, err)
	}

	go func() {
		message, err := io.ReadAll(errorStream)
		switch {
		case err != nil:
			dlog.Errorf(ctx, "error reading from error stream for port %d: %v", remotePort, err)
			pc.onClose()
		case len(message) > 0:
			dlog.Errorf(ctx, "error forwarding to %d: %v", remotePort, string(message))
			pc.onClose()
		}
	}()

	// create data stream
	headers.Set(core.StreamType, core.StreamTypeData)
	dataStream, err = pc.CreateStream(headers)
	if err != nil {
		return nil, fmt.Errorf("error creating forwarding stream for port %d: %v", remotePort, err)
	}
	return &portConn{
		podConn:     pc,
		dataStream:  dataStream,
		errorStream: errorStream,
	}, nil
}

func (pc *podConn) Close() error {
	// Must close before calling onClose, because the close
	// will release a channel that in some situations will
	// block the onClose().
	err := pc.Connection.Close()
	if pc.onClose != nil {
		pc.onClose()
	}
	return err
}

// portConn implements net.Conn and represents a connection to a specific port in a pod.
type portConn struct {
	*podConn
	dataStream  httpstream.Stream
	errorStream httpstream.Stream
}

func (pc *portConn) Read(b []byte) (n int, err error) {
	return pc.dataStream.Read(b)
}

func (pc *portConn) Write(b []byte) (int, error) {
	n, err := pc.dataStream.Write(b)
	return n, err
}

type addr string

func (a addr) Network() string { return "kubectl-port-forward" }
func (a addr) String() string  { return string(a) }

func (pc *portConn) LocalAddr() net.Addr {
	if dataConn, ok := pc.dataStream.(net.Conn); ok {
		return dataConn.LocalAddr()
	}
	return addr("client")
}

func (pc *portConn) RemoteAddr() net.Addr {
	if dataConn, ok := pc.dataStream.(net.Conn); ok {
		return dataConn.RemoteAddr()
	}
	return addr("server")
}

func (pc *portConn) Close() error {
	pc.dataStream.Close()
	pc.errorStream.Close()
	pc.RemoveStreams(pc.dataStream, pc.errorStream)
	if atomic.AddInt64(&pc.refCount, -1) == 0 {
		pc.Close()
	}
	return nil
}

func (pc *portConn) SetDeadline(t time.Time) error {
	if dataConn, ok := pc.dataStream.(net.Conn); ok {
		return dataConn.SetDeadline(t)
	}
	return nil
}

func (pc *portConn) SetReadDeadline(t time.Time) error {
	if dataConn, ok := pc.dataStream.(net.Conn); ok {
		return dataConn.SetReadDeadline(t)
	}
	return nil
}

func (pc *portConn) SetWriteDeadline(t time.Time) error {
	if dataConn, ok := pc.dataStream.(net.Conn); ok {
		return dataConn.SetWriteDeadline(t)
	}
	return nil
}
