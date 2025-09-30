package forwarder

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type interceptWithFilters struct {
	intercept     *manager.InterceptInfo
	headerFilters map[string]string
	pathFilters   []string
}

type httpInterceptor struct {
	interceptor
	intercepts     []*interceptWithFilters
	originalTarget netip.AddrPort
}

func (h *httpInterceptor) SetIntercepting(ctx context.Context, info *manager.InterceptInfo) {
	if info == nil {
		h.SetInterceptingMultiple(ctx, nil)
		return
	}
	h.SetInterceptingMultiple(ctx, []*manager.InterceptInfo{info})
}

func (h *httpInterceptor) SetInterceptingMultiple(ctx context.Context, infos []*manager.InterceptInfo) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Clear existing intercepts
	h.intercepts = nil
	h.intercept = nil

	if len(infos) == 0 {
		dlog.Debugf(ctx, "HTTP interceptor cleared")
		return
	}

	// Set up multiple intercepts
	h.intercepts = make([]*interceptWithFilters, 0, len(infos))
	for _, info := range infos {
		if spec := info.Spec; spec != nil {
			h.intercepts = append(h.intercepts, &interceptWithFilters{
				intercept:     info,
				headerFilters: spec.HeaderFilters,
				pathFilters:   spec.PathFilters,
			})
		}
	}

	dlog.Debugf(ctx, "HTTP interceptor configured with %d intercepts", len(h.intercepts))
}

func (h *httpInterceptor) Serve(ctx context.Context, initCh chan<- netip.AddrPort) error {
	listener, err := h.listen(ctx)
	if err != nil {
		return err
	}
	defer listener.Close()

	la := listener.Addr().(*net.TCPAddr)
	if initCh != nil {
		initCh <- la.AddrPort()
		close(initCh)
	}

	dlog.Debugf(ctx, "HTTP interceptor forwarding from %s", la)
	defer dlog.Debugf(ctx, "Done HTTP interceptor forwarding from %s", la)

	go h.acceptLoop(listener)
	<-ctx.Done()
	return nil
}

func (h *httpInterceptor) listen(ctx context.Context) (*net.TCPListener, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Set up listener lifetime (same as the overall forwarder lifetime)
	h.lCtx, h.lCancel = context.WithCancel(ctx)

	// Set up a target lifetime
	h.tCtx, h.tCancel = context.WithCancel(h.lCtx)
	listenPort := h.listenPort

	listener, err := net.ListenTCP("tcp", &net.TCPAddr{Port: int(listenPort)})
	if err != nil {
		return nil, err
	}
	addr := listener.Addr().(*net.TCPAddr).AddrPort()
	h.lCtx = dlog.WithField(h.lCtx, "listen", addr.String())
	h.listenPort = addr.Port()
	return listener, nil
}

func (h *httpInterceptor) acceptLoop(listener *net.TCPListener) {
	for {
		select {
		case <-h.lCtx.Done():
			return
		default:
		}

		conn, err := listener.AcceptTCP()
		if err != nil {
			if h.lCtx.Err() != nil {
				return
			}
			dlog.Infof(h.lCtx, "Error on accept: %+v", err)
			continue
		}
		go func() {
			if err := h.handleHTTPConn(conn); err != nil {
				dlog.Error(h.lCtx, err)
			}
		}()
	}
}

func (h *httpInterceptor) handleHTTPConn(clientConn net.Conn) error {
	h.mu.Lock()
	ctx := h.tCtx
	originalTarget := h.originalTarget
	// Copy intercepts to avoid holding lock during request processing
	intercepts := make([]*interceptWithFilters, len(h.intercepts))
	copy(intercepts, h.intercepts)
	h.mu.Unlock()

	ctx = dlog.WithField(ctx, "client", clientConn.RemoteAddr().String())
	defer clientConn.Close()

	// Read the HTTP request
	reader := bufio.NewReader(clientConn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return fmt.Errorf("failed to read HTTP request: %w", err)
	}

	// Check each intercept to see if it matches this request
	// Use precedence model: headers take priority over path-only intercepts

	// Pass 1: Check intercepts with headers (high priority tier)
	for _, interceptInfo := range intercepts {
		if len(interceptInfo.headerFilters) > 0 {
			if h.shouldInterceptRequest(ctx, req, interceptInfo.headerFilters, interceptInfo.pathFilters) {
				dlog.Debugf(ctx, "Intercepting HTTP request %s %s with header-based intercept %s",
					req.Method, req.URL.Path, interceptInfo.intercept.Id)
				return h.interceptHTTPConn(ctx, clientConn, req, interceptInfo.intercept)
			}
		}
	}

	// Pass 2: Check intercepts with only paths (low priority tier)
	for _, interceptInfo := range intercepts {
		if len(interceptInfo.headerFilters) == 0 && len(interceptInfo.pathFilters) > 0 {
			if h.shouldInterceptRequest(ctx, req, interceptInfo.headerFilters, interceptInfo.pathFilters) {
				dlog.Debugf(ctx, "Intercepting HTTP request %s %s with path-based intercept %s",
					req.Method, req.URL.Path, interceptInfo.intercept.Id)
				return h.interceptHTTPConn(ctx, clientConn, req, interceptInfo.intercept)
			}
		}
	}

	// No intercepts matched, forward to original service
	dlog.Debugf(ctx, "Forwarding HTTP request %s %s to original service (no intercepts matched)", req.Method, req.URL.Path)
	return h.forwardToOriginalService(ctx, clientConn, req, originalTarget)
}

func (h *httpInterceptor) shouldInterceptRequest(ctx context.Context, req *http.Request, headerFilters map[string]string, pathFilters []string) bool {
	// Check header filters (AND logic - all must match)
	for key, expectedValue := range headerFilters {
		actualValue := req.Header.Get(key)
		if !h.matchesPattern(actualValue, expectedValue) {
			dlog.Debugf(ctx, "Request header %s=%s does not match filter %s=%s",
				key, actualValue, key, expectedValue)
			return false
		}
	}

	// Check path filters (OR logic - any must match)
	if len(pathFilters) > 0 {
		pathMatched := false
		for _, filter := range pathFilters {
			matched := false
			switch {
			case strings.HasPrefix(filter, ":path-equal:"):
				// Exact match
				path := strings.TrimPrefix(filter, ":path-equal:")
				matched = req.URL.Path == path
			case strings.HasPrefix(filter, ":path-prefix:"):
				// Prefix match
				prefix := strings.TrimPrefix(filter, ":path-prefix:")
				matched = strings.HasPrefix(req.URL.Path, prefix)
			case strings.HasPrefix(filter, ":path-regex:"):
				// Regex match
				pattern := strings.TrimPrefix(filter, ":path-regex:")
				if re, err := regexp.Compile(pattern); err == nil {
					matched = re.MatchString(req.URL.Path)
				} else {
					dlog.Debugf(ctx, "Invalid regex pattern %s: %v", pattern, err)
				}
			}

			if matched {
				pathMatched = true
				break
			}
		}
		if !pathMatched {
			dlog.Debugf(ctx, "Request path %s does not match any path filters", req.URL.Path)
			return false
		}
	}

	return true
}

func (h *httpInterceptor) matchesPattern(value, pattern string) bool {
	// Support wildcard matching with *
	if strings.Contains(pattern, "*") {
		matched, _ := filepath.Match(pattern, value)
		return matched
	}
	// Exact match
	return value == pattern
}

func (h *httpInterceptor) interceptHTTPConn(ctx context.Context, clientConn net.Conn, req *http.Request, iCept *manager.InterceptInfo) error {
	spec := iCept.Spec
	ip, err := iputil.ParseAddr(spec.TargetHost)
	if err != nil {
		return err
	}

	// Convert the connection to intercept through tunnel
	return h.rerouteHTTPConn(
		ctx,
		clientConn,
		req,
		tunnel.SessionID(iCept.ClientSession.SessionId),
		netip.AddrPortFrom(ip, uint16(spec.TargetPort)),
		time.Duration(spec.RoundtripLatency),
		time.Duration(spec.DialTimeout))
}

func (h *httpInterceptor) rerouteHTTPConn(
	ctx context.Context, conn net.Conn, req *http.Request, clientSession tunnel.SessionID,
	dst netip.AddrPort, latency, timeout time.Duration,
) error {
	srcAddr := conn.RemoteAddr()
	dlog.Debugf(ctx, "Intercepting HTTP connection from %s", srcAddr)
	defer dlog.Debugf(ctx, "Done intercepting HTTP connection from %s", srcAddr)

	src, err := iputil.SplitToIPPort(conn.RemoteAddr())
	if err != nil {
		return fmt.Errorf("failed to parse intercept source address %s: %w", srcAddr, err)
	}

	proto, err := types.ParseProto(srcAddr.Network())
	if err != nil {
		return fmt.Errorf("failed to parse intercept protocol %s: %w", srcAddr, err)
	}
	id := tunnel.NewConnID(proto, src, dst)
	ctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	sp := h.streamProvider
	h.mu.Unlock()
	s, err := sp.CreateClientStream(ctx, tunnel.AgentToClient, clientSession, id, latency, timeout)
	if err != nil {
		cancel()
		return err
	}

	// Re-serialize the HTTP request and send it through the tunnel
	var requestBuf strings.Builder
	if err := req.Write(&requestBuf); err != nil {
		cancel()
		return fmt.Errorf("failed to serialize HTTP request: %w", err)
	}

	// Create a connection that starts with the HTTP request
	wrappedConn := &httpPrefixConn{
		Conn:   conn,
		prefix: []byte(requestBuf.String()),
	}

	ingressBytes := tunnel.NewCounterProbe("FromClientBytes")
	egressBytes := tunnel.NewCounterProbe("ToClientBytes")

	// Ingress and egress swap places here, because this endpoint reflects a connection
	// where the stream is attached to a connection *to* the client, not *from* the client.
	d := tunnel.NewConnEndpoint(s, wrappedConn, cancel, egressBytes, ingressBytes)
	d.Start(ctx)
	<-d.Done()

	sp.ReportMetrics(ctx, &manager.TunnelMetrics{
		ClientSessionId: string(clientSession),
		IngressBytes:    ingressBytes.GetValue(),
		EgressBytes:     egressBytes.GetValue(),
	})
	return nil
}

func (h *httpInterceptor) forwardToOriginalService(ctx context.Context, clientConn net.Conn, req *http.Request, target netip.AddrPort) error {
	defer clientConn.Close()

	if target.Port() == 0 {
		dlog.Debug(ctx, "Forwarding to /dev/null")
		_, _ = io.Copy(io.Discard, clientConn)
		return nil
	}

	// Connect to original service
	targetAddr := net.TCPAddrFromAddrPort(target)

	targetConn, err := net.DialTCP("tcp", nil, targetAddr)
	if err != nil {
		return fmt.Errorf("error dialing original service: %w", err)
	}
	defer targetConn.Close()

	ctx = dlog.WithField(ctx, "target", targetAddr.String())
	dlog.Debug(ctx, "Forwarding to original service...")

	// Send the HTTP request to the original service
	if err := req.Write(targetConn); err != nil {
		return fmt.Errorf("error writing request to original service: %w", err)
	}

	// Relay data bidirectionally
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

// httpPrefixConn wraps a connection and prefixes reads with HTTP request data.
type httpPrefixConn struct {
	net.Conn
	prefix     []byte
	prefixRead bool
	mu         sync.Mutex
}

func (c *httpPrefixConn) Read(b []byte) (n int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.prefixRead && len(c.prefix) > 0 {
		n = copy(b, c.prefix)
		if n < len(c.prefix) {
			c.prefix = c.prefix[n:]
		} else {
			c.prefixRead = true
		}
		return n, nil
	}

	return c.Conn.Read(b)
}

// DispatchByMechanism is a no-op for the HTTP interceptor since it already represents
// the mechanism-specific interceptor. It returns false to indicate the caller should
// continue with its normal handling.
func (h *httpInterceptor) DispatchByMechanism(_ context.Context, _ net.Conn, _ *manager.InterceptInfo) (bool, error) {
	return false, nil
}
