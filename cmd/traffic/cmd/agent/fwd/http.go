package fwd

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func (f *tcp) acceptHTTPLoop(ctx context.Context, listener net.Listener) {
	la := listener.Addr().(*net.TCPAddr)
	defaultHandler := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: f.Target().String()})

	server := &http.Server{
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			f.handleHTTPRequest(writer, request, defaultHandler)
		}),
	}

	go func() {
		<-ctx.Done()
		if err := server.Shutdown(context.WithoutCancel(ctx)); err != nil {
			dlog.Errorf(ctx, "Error shutting down HTTP forwarder: %v", err)
		}
	}()
	dlog.Debugf(ctx, "Starting HTTP intercept forwarder on %s", la)
	defer dlog.Debugf(ctx, "Done HTTP interceptor forwarding from %s", la)
	err := server.Serve(listener)
	if err != nil {
		dlog.Errorf(ctx, "Error serving HTTP intercept: %v", err)
	}
}

func (f *tcp) handleHTTPRequest(writer http.ResponseWriter, req *http.Request, defaultHandler http.Handler) {
	// Copy wiretaps and intercepts to avoid holding a lock during request processing
	f.mu.Lock()
	wtIntercepts := f.wiretaps.sorted()
	intercepts := f.intercepts.sorted()
	f.mu.Unlock()

	dlog.Debugf(f.lCtx, "Handling %s %s %s", req.Proto, req.Method, req.URL.Path)
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
			taps, err := addRequestTaps(f.lCtx, req, tapCount, 1024)
			if err != nil {
				dlog.Errorf(f.lCtx, "Failed to add request taps: %v", err)
			} else {
				for i, ii := range wts {
					f.serveTap(ii.ctx, src, taps[i], ii.InterceptInfo)
				}
			}
		}
	}

	// Pass 1: Check intercepts with headers (high priority tier)
	for _, ic := range intercepts {
		spec := ic.Spec
		if len(spec.HeaderFilters) > 0 {
			if shouldInterceptRequest(req, spec.HeaderFilters, spec.PathFilters) {
				dlog.Debugf(f.lCtx, "Intercepting HTTP request %s %s with header-based intercept %s",
					req.Method, req.URL.Path, ic.Id)
				f.serveHTTPIntercept(ic.ctx, src, writer, req, ic.InterceptInfo)
				return
			}
		}
	}

	// Pass 2: Check intercepts with only paths (low priority tier)
	for _, ic := range intercepts {
		spec := ic.Spec
		if len(spec.HeaderFilters) == 0 && len(spec.PathFilters) > 0 {
			if shouldInterceptRequest(req, spec.HeaderFilters, spec.PathFilters) {
				dlog.Debugf(f.lCtx, "Intercepting HTTP request %s %s with path-based intercept %s",
					req.Method, req.URL.Path, ic.Id)
				f.serveHTTPIntercept(ic.ctx, src, writer, req, ic.InterceptInfo)
				return
			}
		}
	}
	defaultHandler.ServeHTTP(writer, req)
}

func shouldInterceptRequest(req *http.Request, headerFilters map[string]string, pathFilters []string) bool {
	return matcher.NewRequest(pathFilters, headerFilters).Matches(req)
}

func (f *tcp) serveHTTPIntercept(ctx context.Context, src netip.AddrPort, writer http.ResponseWriter, request *http.Request, ii *manager.InterceptInfo) {
	spec := ii.Spec
	trn := http.DefaultTransport.(*http.Transport).Clone()
	trn.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
		s, err := f.createStream(ctx, src, ii)
		if err != nil {
			return nil, err
		}
		ingressBytes := tunnel.NewCounterProbe("FromClientBytes")
		egressBytes := tunnel.NewCounterProbe("ToClientBytes")
		return tunnel.NewStreamConn(ctx, s, ingressBytes, egressBytes), nil
	}

	targetProxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: spec.TargetHost})
	targetProxy.Transport = trn
	targetProxy.ServeHTTP(writer, request)
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
				dlog.Errorf(ctx, "Failed to read from tap: %v", err)
			}
			break
		}
		err = s.Send(ctx, tunnel.NewMessage(tunnel.Normal, buf[:n]))
		if err != nil {
			dlog.Errorf(ctx, "Failed to send to stream: %v", err)
			break
		}
	}
}
