package fwd

import (
	"context"
	"crypto/tls"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"sync"

	"golang.org/x/net/http2"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

const (
	httpInterceptMaxIdleConns        = 512
	httpInterceptMaxIdleConnsPerHost = 128
	httpInterceptMaxConnsPerHost     = 128
)

type httpInterceptDialContextKey struct{}

type httpInterceptDialContext struct {
	src netip.AddrPort
	ii  *manager.InterceptInfo
}

type httpInterceptTransport struct {
	targetURL *url.URL
	transport *http.Transport
}

type metricsReportingConn struct {
	net.Conn
	once   sync.Once
	report func()
}

func (c *metricsReportingConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.report)
	return err
}

func (f *tcp) protocols(ctx context.Context, plainText bool) *http.Protocols {
	pr := new(http.Protocols)
	pr.SetHTTP1(true)
	if tm := f.tlsManager; tm != nil {
		tp := f.Target().Port()
		if tm.UseHTTP2(ctx, tp) {
			if !plainText && tm.UseTLS(ctx, tp) {
				pr.SetHTTP2(true)
			} else {
				pr.SetUnencryptedHTTP2(true)
			}
		}
	}
	return pr
}

func (f *tcp) configureTransport(ctx context.Context, plainText bool) *http.Transport {
	trn := http.DefaultTransport.(*http.Transport).Clone()
	trn.Protocols = f.protocols(ctx, plainText)
	if trn.Protocols.UnencryptedHTTP2() {
		trn.Protocols.SetHTTP1(false)
	}
	// A node-agent forwarder dials its pass-through target inside the target
	// pod's network namespace; without this the reverse proxy would dial the
	// target pod IP from the node-agent's own namespace and never traverse the
	// proxy-port DNAT.
	if d := f.Dialer(); d != nil {
		trn.DialContext = d.DialContext
	}
	return trn
}

func (f *tcp) targetUsesTLS(ctx context.Context) bool {
	if tm := f.tlsManager; tm != nil && tm.UseTLS(ctx, f.Target().Port()) {
		return true
	}
	return false
}

func (f *tcp) configureDownstreamTLS(ctx context.Context, server *http.Server, listener net.Listener) (net.Listener, error) {
	tm := f.tlsManager
	tp := f.Target().Port()
	if tm == nil || !tm.UseTLS(ctx, tp) {
		return listener, nil
	}
	cert := tm.GetDownstreamCertificate(tp)
	if cert == nil {
		return listener, nil
	}
	server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{*cert}}
	listener = tls.NewListener(listener, server.TLSConfig)
	err := http2.ConfigureServer(server, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to configure HTTP2 server: %v", err)
	}
	return listener, nil
}

func (f *tcp) acceptHTTPLoop(ctx context.Context, listener net.Listener) {
	la := listener.Addr().(*net.TCPAddr)
	scheme := "http"
	if f.targetUsesTLS(ctx) {
		scheme = "https"
	}
	defaultHandler := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: scheme, Host: f.Target().String()})
	defaultHandler.ErrorHandler = proxyErrorHandler
	defaultHandler.Transport = f.configureTransport(ctx, false)

	server := &http.Server{
		BaseContext: func(_ net.Listener) context.Context {
			return f.lCtx
		},
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			f.handleHTTPRequest(writer, request, defaultHandler)
		}),
		Protocols: f.protocols(ctx, false),
	}

	var err error
	listener, err = f.configureDownstreamTLS(ctx, server, listener)
	if err != nil {
		return
	}

	go func() {
		clog.Debugf(ctx, "Starting HTTP intercept forwarder on %s", la)
		defer clog.Debugf(ctx, "Done HTTP interceptor forwarding from %s", la)

		if err := server.Serve(listener); err != nil {
			clog.Errorf(ctx, "Error serving HTTP intercept: %v", err)
		}
	}()

	<-ctx.Done()
	if err := server.Shutdown(context.WithoutCancel(ctx)); err != nil {
		clog.Errorf(ctx, "Error shutting down HTTP forwarder: %v", err)
	}
}

func (f *tcp) handleHTTPRequest(writer http.ResponseWriter, req *http.Request, defaultHandler http.Handler) {
	// Copy wiretaps and intercepts to avoid holding a lock during request processing
	f.mu.Lock()
	wtIntercepts := f.wiretaps.sorted()
	intercepts := f.intercepts.sorted()
	f.mu.Unlock()

	clog.Debugf(f.lCtx, "Handling %s %s %s", req.Proto, req.Method, req.URL.Path)
	src, err := netip.ParseAddrPort(req.RemoteAddr)
	if err != nil {
		src = netip.AddrPortFrom(netip.IPv4Unspecified(), 0)
	}

	// Check each intercept to see if it matches this request
	// No precedence here because taps are not conflicting.
	if len(wtIntercepts) > 0 {
		wts := make([]*interceptController, 0, len(wtIntercepts))
		for _, ic := range wtIntercepts {
			spec := ic.Spec
			if shouldInterceptRequest(req, spec.HeaderFilters, spec.PathFilters) {
				wts = append(wts, ic)
			}
		}
		if tapCount := len(wts); tapCount > 0 {
			taps, tapReader, err := addRequestTaps(f.lCtx, req, tapCount, wiretapCacheSize)
			if err != nil {
				clog.Errorf(f.lCtx, "Failed to add request taps: %v", err)
			} else {
				// A wiretap is lossy and best-effort and must never block or
				// delay the real request. Nothing guarantees the request body
				// is ever read (a bodyless GET is never read by
				// httputil.ReverseProxy), so closeTaps is called unconditionally
				// once the request has been served, regardless of which return
				// path was taken and regardless of whether the body was read.
				// It is idempotent, so it is harmless if the body was already
				// read to completion (which independently sends the taps a
				// terminal EOF) or the server itself later closes the body.
				// The tap goroutines below are intentionally not awaited: they
				// finish shortly after the handler returns, bounded by the
				// per-intercept context and the pipe closing.
				defer tapReader.closeTaps()
				for i, ii := range wts {
					go func(tap io.Reader, ii *interceptController) {
						f.serveTap(ii.ctx, src, tap, ii.InterceptInfo)
					}(taps[i], ii)
				}
			}
		}
	}

	// Pass 1: Check intercepts with headers (high-priority tier)
	for _, ic := range intercepts {
		spec := ic.Spec
		if len(spec.HeaderFilters) > 0 {
			if shouldInterceptRequest(req, spec.HeaderFilters, spec.PathFilters) {
				clog.Debugf(f.lCtx, "Intercepting HTTP request %s %s with header-based intercept %s",
					req.Method, req.URL.Path, ic.Id)
				f.serveHTTPIntercept(ic.ctx, src, writer, req, ic.InterceptInfo, defaultHandler)
				return
			}
		}
	}

	// Pass 2: Check intercepts with only paths (low-priority tier)
	for _, ic := range intercepts {
		spec := ic.Spec
		if len(spec.HeaderFilters) == 0 && len(spec.PathFilters) > 0 {
			if shouldInterceptRequest(req, spec.HeaderFilters, spec.PathFilters) {
				clog.Debugf(f.lCtx, "Intercepting HTTP request %s %s with path-based intercept %s",
					req.Method, req.URL.Path, ic.Id)
				f.serveHTTPIntercept(ic.ctx, src, writer, req, ic.InterceptInfo, defaultHandler)
				return
			}
		}
	}
	defaultHandler.ServeHTTP(writer, req)
}

func shouldInterceptRequest(req *http.Request, headerFilters map[string]string, pathFilters []string) bool {
	return matcher.NewRequest(pathFilters, headerFilters).Matches(req)
}

func (f *tcp) configureUpstreamTransport(ctx context.Context, plaintext bool) *http.Transport {
	tm := f.tlsManager
	tp := f.Target().Port()
	trn := f.configureTransport(ctx, plaintext)
	trn.MaxIdleConns = max(trn.MaxIdleConns, httpInterceptMaxIdleConns)
	trn.MaxIdleConnsPerHost = max(trn.MaxIdleConnsPerHost, httpInterceptMaxIdleConnsPerHost)
	trn.MaxConnsPerHost = max(trn.MaxConnsPerHost, httpInterceptMaxConnsPerHost)
	if plaintext {
		return trn
	}
	if tm == nil || !tm.UseTLS(ctx, tp) {
		return trn
	}
	cert, useISV := tm.GetUpstreamCertificate(tp)
	if !useISV && cert == nil {
		return trn
	}
	var certs []tls.Certificate
	if cert != nil {
		certs = []tls.Certificate{*cert}
	}
	trn.TLSClientConfig = &tls.Config{Certificates: certs, InsecureSkipVerify: useISV}
	return trn
}

func httpInterceptTransportKey(ii *manager.InterceptInfo, requestProtoMajor int) string {
	if ii == nil || ii.Spec == nil || ii.ClientSession == nil {
		return ""
	}
	protocol := "h2"
	if requestProtoMajor < 2 {
		protocol = "h1"
	}
	spec := ii.Spec
	return fmt.Sprintf(
		"%s|%s|%s|%d|%t|%s",
		ii.Id,
		ii.ClientSession.SessionId,
		spec.TargetHost,
		spec.TargetPort,
		spec.Plaintext,
		protocol,
	)
}

func (f *tcp) getHTTPInterceptTransport(
	ctx context.Context,
	ii *manager.InterceptInfo,
	requestProtoMajor int,
) *httpInterceptTransport {
	key := httpInterceptTransportKey(ii, requestProtoMajor)
	if cached, ok := f.httpTransportCache.Load(key); ok {
		return cached.(*httpInterceptTransport)
	}

	spec := ii.Spec
	trn := f.configureUpstreamTransport(ctx, spec.Plaintext)

	// Keep the protocol choice tied to the request being forwarded. In
	// particular, an HTTP/1 request must not reuse a transport configured for
	// h2c prior knowledge.
	if requestProtoMajor < 2 && trn.Protocols.UnencryptedHTTP2() {
		pr := new(http.Protocols)
		pr.SetHTTP1(true)
		trn.Protocols = pr
	}

	trn.DialContext = func(dialCtx context.Context, _, _ string) (net.Conn, error) {
		di, ok := dialCtx.Value(httpInterceptDialContextKey{}).(*httpInterceptDialContext)
		if !ok {
			return nil, fmt.Errorf("missing HTTP selected intercept dial context for intercept %s", ii.Id)
		}

		f.mu.Lock()
		sp := f.streamProvider
		f.mu.Unlock()

		metricsEnabled := sp != nil && sp.MetricsEnabled()
		var ingressBytes, egressBytes *tunnel.CounterProbe
		if metricsEnabled {
			ingressBytes = tunnel.NewCounterProbe("FromClientBytes")
			egressBytes = tunnel.NewCounterProbe("ToClientBytes")
		}

		s, err := f.createStream(dialCtx, di.src, di.ii)
		if err != nil {
			return nil, err
		}
		// Ingress and egress swap places here because this is a connection where the stream is attached to a connection *to* the client, not *from* the client.
		conn := tunnel.NewStreamConn(dialCtx, s, egressBytes, ingressBytes)
		if !metricsEnabled {
			return conn, nil
		}
		return &metricsReportingConn{
			Conn: conn,
			report: func() {
				sp.ReportMetrics(f.lCtx, &manager.TunnelMetrics{
					ClientSessionId: di.ii.ClientSession.SessionId,
					IngressBytes:    ingressBytes.GetValue(),
					EgressBytes:     egressBytes.GetValue(),
				})
			},
		}, nil
	}

	scheme := "http"
	if trn.Protocols.HTTP2() {
		scheme = "https"
	}
	hit := &httpInterceptTransport{
		targetURL: &url.URL{Scheme: scheme, Host: iputil.JoinHostPort(spec.TargetHost, uint16(spec.TargetPort))},
		transport: trn,
	}
	actual, loaded := f.httpTransportCache.LoadOrStore(key, hit)
	if loaded {
		trn.CloseIdleConnections()
		return actual.(*httpInterceptTransport)
	}
	return hit
}

func (f *tcp) pruneHTTPInterceptTransports(intercepts []*manager.InterceptInfo) {
	if len(intercepts) == 0 {
		f.httpTransportCache.Range(func(key, value any) bool {
			if hit, ok := value.(*httpInterceptTransport); ok {
				hit.transport.CloseIdleConnections()
			}
			f.httpTransportCache.Delete(key)
			return true
		})
		return
	}

	active := make(map[string]struct{}, len(intercepts)*2)
	for _, ii := range intercepts {
		for _, protoMajor := range []int{1, 2} {
			if key := httpInterceptTransportKey(ii, protoMajor); key != "" {
				active[key] = struct{}{}
			}
		}
	}
	f.httpTransportCache.Range(func(key, value any) bool {
		if _, ok := active[key.(string)]; ok {
			return true
		}
		if hit, ok := value.(*httpInterceptTransport); ok {
			hit.transport.CloseIdleConnections()
		}
		f.httpTransportCache.Delete(key)
		return true
	})
}

func (f *tcp) serveHTTPIntercept(
	ctx context.Context,
	src netip.AddrPort,
	writer http.ResponseWriter,
	request *http.Request,
	ii *manager.InterceptInfo,
	defaultHandler http.Handler,
) {
	hit := f.getHTTPInterceptTransport(ctx, ii, request.ProtoMajor)

	if tlsConfig := hit.transport.TLSClientConfig; tlsConfig != nil {
		if len(tlsConfig.Certificates) > 0 {
			clog.Debugf(ctx, "Using a client certificate when connecting to %s", hit.targetURL)
		} else {
			clog.Debugf(ctx, "Not using a client certificate when connecting to %s", hit.targetURL)
		}
		if tlsConfig.InsecureSkipVerify {
			clog.Warnf(ctx, "Skipping verification of server's certificate chain and host name when connecting to %s", hit.targetURL)
		}
	} else {
		clog.Debugf(ctx, "No TLS config used when connecting to %s", hit.targetURL)
	}
	targetProxy := httputil.NewSingleHostReverseProxy(hit.targetURL)
	targetProxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		if errors.Is(err, errClientStream) {
			clog.Warnf(ctx, "Intercept tunnel unavailable for %s %s; failing open to app container: %v", req.Method, req.URL.Path, err)
			defaultHandler.ServeHTTP(rw, req)
			return
		}
		proxyErrorHandler(rw, req, err)
	}
	targetProxy.Transport = hit.transport
	request = request.WithContext(context.WithValue(request.Context(), httpInterceptDialContextKey{}, &httpInterceptDialContext{
		src: src,
		ii:  ii,
	}))
	targetProxy.ServeHTTP(writer, request)

	clog.Debugf(ctx, "Request to %s ended", hit.targetURL)
}

func proxyErrorHandler(rw http.ResponseWriter, _ *http.Request, err error) {
	type httpError struct {
		Error string `json:"error"`
	}
	h := httpError{Error: err.Error()}
	b, _ := json.Marshal(h)
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Content-Length", fmt.Sprintf("%d", len(b)))
	rw.WriteHeader(http.StatusBadGateway)
	_, _ = rw.Write(b)
}

func (f *tcp) serveTap(ctx context.Context, src netip.AddrPort, tap io.Reader, ii *manager.InterceptInfo) {
	s, err := f.createStream(ctx, src, ii)
	if err != nil {
		return
	}
	buf := make([]byte, 4096)
	for {
		n, err := tap.Read(buf)
		if err != nil {
			if err != io.EOF {
				clog.Errorf(ctx, "Failed to read from tap: %v", err)
			}
			break
		}
		err = s.Send(ctx, tunnel.NewMessage(tunnel.Normal, buf[:n]))
		if err != nil {
			clog.Errorf(ctx, "Failed to send to stream: %v", err)
			break
		}
	}
}
