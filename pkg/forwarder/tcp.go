package forwarder

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type tcp struct {
	interceptor
}

func newTCP(listenPort uint16, tag tunnel.Tag, targetHost string, targetPort uint16) Interceptor {
	return &tcp{
		interceptor: interceptor{
			tag:        tag,
			listenPort: listenPort,
			targetHost: targetHost,
			targetPort: targetPort,
		},
	}
}

func (f *tcp) Serve(ctx context.Context, initCh chan<- netip.AddrPort) error {
	listener, err := f.listen(ctx)
	if err != nil {
		return err
	}
	defer listener.Close()

	la := listener.Addr().(*net.TCPAddr)
	if initCh != nil {
		initCh <- la.AddrPort()
		close(initCh)
	}

	dlog.Debugf(ctx, "Forwarding from %s", la)
	defer dlog.Debugf(ctx, "Done forwarding from %s", la)

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		conn, err := listener.AcceptTCP()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			dlog.Infof(ctx, "Error on accept: %+v", err)
			continue
		}
		go func() {
			if err := f.forwardConn(conn); err != nil {
				dlog.Error(ctx, err)
			}
		}()
	}
}

func (f *tcp) listen(ctx context.Context) (*net.TCPListener, error) {
	f.mu.Lock()

	// Set up listener lifetime (same as the overall forwarder lifetime)
	f.lCtx, f.lCancel = context.WithCancel(ctx)
	f.lCtx = dlog.WithField(f.lCtx, "lis", f.listenPort)

	// Set up target lifetime
	f.tCtx, f.tCancel = context.WithCancel(f.lCtx)
	listenPort := f.listenPort

	f.mu.Unlock()
	return net.ListenTCP("tcp", &net.TCPAddr{Port: int(listenPort)})
}

// Number of []byte chunks that can be cached by a wiretap connection before it discards data.
const wiretapCacheSize = 0x100

func (f *tcp) forwardConn(clientConn net.Conn) error {
	var wtIntercepts []*manager.InterceptInfo
	f.mu.Lock()
	ctx := f.tCtx
	targetHost := f.targetHost
	targetPort := f.targetPort
	intercept := f.intercept
	tapCount := len(f.wiretaps)
	if tapCount > 0 {
		wtIntercepts = make([]*manager.InterceptInfo, tapCount)
		i := 0
		for _, wt := range f.wiretaps {
			wtIntercepts[i] = wt
			i++
		}
	}
	f.mu.Unlock()

	ctx = dlog.WithField(ctx, "client", clientConn.RemoteAddr().String())

	var targetAddr *net.TCPAddr
	if targetPort > 0 {
		var err error
		hp := iputil.JoinHostPort(targetHost, targetPort)
		targetAddr, err = net.ResolveTCPAddr("tcp", hp)
		if err != nil {
			return fmt.Errorf("error on resolve(%s): %w", hp, err)
		}

		if len(wtIntercepts) > 0 && targetPort > 0 {
			var taps []net.Conn
			clientConn, taps = AddWiretaps(ctx, clientConn, tapCount, wiretapCacheSize)
			for i, ii := range wtIntercepts {
				go func(conn net.Conn, intercept *manager.InterceptInfo) {
					err := f.interceptConn(ctx, conn, intercept)
					if err != nil {
						dlog.Errorf(ctx, "wiretap ended with error: %v", err)
					}
				}(taps[i], ii)
			}
		}
	}
	if intercept != nil {
		return f.interceptConn(ctx, clientConn, intercept)
	}

	defer dlog.Debug(ctx, "Done forwarding")
	defer clientConn.Close()

	if targetPort == 0 {
		dlog.Debug(ctx, "Forwarding to /dev/null")
		_, _ = io.Copy(io.Discard, clientConn)
		return nil
	}

	ctx = dlog.WithField(ctx, "target", targetAddr.String())

	dlog.Debug(ctx, "Forwarding...")

	targetConn, err := net.DialTCP("tcp", nil, targetAddr)
	if err != nil {
		return fmt.Errorf("error on dial: %w", err)
	}
	defer targetConn.Close()

	done := make(chan struct{})

	go func() {
		if _, err := io.Copy(targetConn, clientConn); err != nil && ctx.Err() == nil {
			dlog.Debugf(ctx, "Error clientConn->targetConn: %+v", err)
		}
		_ = targetConn.CloseWrite()
		done <- struct{}{}
	}()
	go func() {
		if _, err := io.Copy(clientConn, targetConn); err != nil && ctx.Err() == nil {
			dlog.Debugf(ctx, "Error targetConn->clientConn: %+v", err)
		}
		if hwCloser, ok := clientConn.(interface{ CloseWrite() error }); ok {
			_ = hwCloser.CloseWrite()
		}
		done <- struct{}{}
	}()

	// Wait for both sides to close the connection
	for numClosed := 0; numClosed < 2; {
		select {
		case <-ctx.Done():
			return nil
		case <-done:
			numClosed++
		}
	}
	return nil
}

func (f *tcp) interceptConn(ctx context.Context, conn net.Conn, iCept *manager.InterceptInfo) error {
	spec := iCept.Spec
	return f.rerouteConn(
		ctx,
		conn,
		tunnel.SessionID(iCept.ClientSession.SessionId),
		netip.AddrPortFrom(iputil.Parse(spec.TargetHost), uint16(spec.TargetPort)),
		time.Duration(spec.RoundtripLatency),
		time.Duration(spec.DialTimeout))
}

func (f *tcp) rerouteConn(ctx context.Context, conn net.Conn, clientSession tunnel.SessionID, dst netip.AddrPort, latency, timeout time.Duration) error {
	srcAddr := conn.RemoteAddr()
	dlog.Debugf(ctx, "Accept got connection from %s", srcAddr)
	defer dlog.Debugf(ctx, "Done serving connection from %s", srcAddr)

	src, err := iputil.SplitToIPPort(conn.RemoteAddr())
	if err != nil {
		return fmt.Errorf("failed to parse intercept source address %s: %w", srcAddr, err)
	}

	id := tunnel.NewConnID(ipproto.Parse(srcAddr.Network()), src, dst)
	ctx, cancel := context.WithCancel(ctx)
	f.mu.Lock()
	sp := f.streamProvider
	f.mu.Unlock()
	s, err := sp.CreateClientStream(ctx, tunnel.AgentToClient, clientSession, id, latency, timeout)
	if err != nil {
		cancel()
		return err
	}

	ingressBytes := tunnel.NewCounterProbe("FromClientBytes")
	egressBytes := tunnel.NewCounterProbe("ToClientBytes")

	// Ingress and egress swap places here, because this endpoint reflects a connection
	// where the stream is attached to a connection *to* the client, not *from* the client.
	d := tunnel.NewConnEndpoint(s, conn, cancel, egressBytes, ingressBytes)
	d.Start(ctx)
	<-d.Done()

	sp.ReportMetrics(ctx, &manager.TunnelMetrics{
		ClientSessionId: string(clientSession),
		IngressBytes:    ingressBytes.GetValue(),
		EgressBytes:     egressBytes.GetValue(),
	})
	return nil
}
