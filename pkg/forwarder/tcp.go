package forwarder

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type tcp struct {
	interceptor
}

func newTCP(listen net.Addr, targetHost string, targetPort uint16) Interceptor {
	return &tcp{
		interceptor: interceptor{
			listenAddr: listen,
			targetHost: targetHost,
			targetPort: targetPort,
		},
	}
}

func (f *tcp) Serve(ctx context.Context, initCh chan<- net.Addr) error {
	listener, err := f.listen(ctx)
	if err != nil {
		return err
	}
	defer listener.Close()

	if initCh != nil {
		initCh <- listener.Addr()
		close(initCh)
	}

	dlog.Debugf(ctx, "Forwarding from %s", f.listenAddr.String())
	defer dlog.Debugf(ctx, "Done forwarding from %s", f.listenAddr.String())

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
	f.lCtx = dlog.WithField(f.lCtx, "lis", f.listenAddr.String())

	// Set up target lifetime
	f.tCtx, f.tCancel = context.WithCancel(f.lCtx)
	listenAddr := f.listenAddr

	f.mu.Unlock()
	return net.ListenTCP("tcp", listenAddr.(*net.TCPAddr))
}

func (f *tcp) forwardConn(clientConn *net.TCPConn) error {
	f.mu.Lock()
	ctx := f.tCtx
	ctx, span := otel.Tracer("").Start(ctx, "forwardConn")
	defer span.End()
	targetHost := f.targetHost
	targetPort := f.targetPort
	intercept := f.intercept
	f.mu.Unlock()
	if intercept != nil {
		return f.interceptConn(ctx, clientConn, intercept)
	}

	targetAddr, err := net.ResolveTCPAddr("tcp", iputil.JoinHostPort(targetHost, targetPort))
	if err != nil {
		return fmt.Errorf("error on resolve(%s): %w", iputil.JoinHostPort(targetHost, targetPort), err)
	}

	span.SetAttributes(
		attribute.String("client", clientConn.RemoteAddr().String()),
		attribute.String("target", targetAddr.String()),
	)
	ctx = dlog.WithField(ctx, "client", clientConn.RemoteAddr().String())
	ctx = dlog.WithField(ctx, "target", targetAddr.String())

	dlog.Debug(ctx, "Forwarding...")
	defer dlog.Debug(ctx, "Done forwarding")

	defer clientConn.Close()

	targetConn, err := net.DialTCP("tcp", nil, targetAddr)
	if err != nil {
		return fmt.Errorf("error on dial: %w", err)
	}
	defer targetConn.Close()

	done := make(chan struct{})

	go func() {
		if _, err := io.Copy(targetConn, clientConn); err != nil {
			dlog.Debugf(ctx, "Error clientConn->targetConn: %+v", err)
		}
		_ = targetConn.CloseWrite()
		done <- struct{}{}
	}()
	go func() {
		if _, err := io.Copy(clientConn, targetConn); err != nil {
			dlog.Debugf(ctx, "Error targetConn->clientConn: %+v", err)
		}
		_ = clientConn.CloseWrite()
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
	ctx, span := otel.Tracer("").Start(ctx, "interceptConn")
	defer span.End()
	tracing.RecordInterceptInfo(span, iCept)
	addr := conn.RemoteAddr()
	dlog.Debugf(ctx, "Accept got connection from %s", addr)
	defer dlog.Debugf(ctx, "Done serving connection from %s", addr)

	srcIp, srcPort, err := iputil.SplitToIPPort(addr)
	if err != nil {
		return fmt.Errorf("failed to parse intercept source address %s: %w", addr, err)
	}

	spec := iCept.Spec
	destIp := iputil.Parse(spec.TargetHost)
	clientSession := iCept.ClientSession.SessionId
	id := tunnel.NewConnID(ipproto.Parse(addr.Network()), srcIp, destIp, srcPort, uint16(spec.TargetPort))
	id.SpanRecord(span)
	ctx, cancel := context.WithCancel(ctx)
	f.mu.Lock()
	sp := f.streamProvider
	f.mu.Unlock()
	s, err := sp.CreateClientStream(ctx, clientSession, id, time.Duration(spec.RoundtripLatency), time.Duration(spec.DialTimeout))
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
		ClientSessionId: clientSession,
		IngressBytes:    ingressBytes.GetValue(),
		EgressBytes:     egressBytes.GetValue(),
	})
	return nil
}
