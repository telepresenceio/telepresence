package fwd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func TestHTTPInterceptor_shouldInterceptRequest(t *testing.T) {
	headerFilters := map[string]string{
		"X-User-ID":     "dev123",
		"X-Environment": "staging",
	}
	pathFilters := []string{":path-prefix:/api/v1/", ":path-prefix:/admin/"}

	tests := []struct {
		name            string
		headers         map[string]string
		path            string
		shouldIntercept bool
	}{
		{
			name: "matching headers and path",
			headers: map[string]string{
				"X-User-ID":     "dev123",
				"X-Environment": "staging",
			},
			path:            "/api/v1/users",
			shouldIntercept: true,
		},
		{
			name: "matching headers but no path filters",
			headers: map[string]string{
				"X-User-ID":     "dev123",
				"X-Environment": "staging",
			},
			path:            "/some/other/path",
			shouldIntercept: false,
		},
		{
			name: "missing required header",
			headers: map[string]string{
				"X-User-ID": "dev123",
				// Missing X-Environment
			},
			path:            "/api/v1/users",
			shouldIntercept: false,
		},
		{
			name: "wrong header value",
			headers: map[string]string{
				"X-User-ID":     "prod456",
				"X-Environment": "staging",
			},
			path:            "/api/v1/users",
			shouldIntercept: false,
		},
		{
			name: "wildcard header match",
			headers: map[string]string{
				"X-User-ID":     "dev456",
				"X-Environment": "staging",
			},
			path:            "/api/v1/users",
			shouldIntercept: false, // Exact match required by default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, "http://example.com"+tt.path, nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			result := shouldInterceptRequest(req, headerFilters, pathFilters)
			assert.Equal(t, tt.shouldIntercept, result)
		})
	}
}

func TestHTTPInterceptor_matchesPattern(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		pattern string
		matches bool
	}{
		{"exact match", "dev123", "dev123", true},
		{"no match", "dev123", "prod456", false},
		{"wildcard match", "dev123", "dev.*", true},
		{"wildcard no match", "prod123", "dev.*", false},
		{"complex wildcard", "dev-user-123", "dev-.*-123", true},
		{"empty value", "", "dev*", false},
		{"empty pattern", "dev123", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := matcher.NewValue(tt.pattern).Matches(tt.value)
			assert.Equal(t, tt.matches, result)
		})
	}
}

func TestHTTPInterceptor_noFilters(t *testing.T) {
	headerFilters := map[string]string{}
	pathFilters := []string{}

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/any/path", nil)

	// No filters means intercept everything
	result := shouldInterceptRequest(req, headerFilters, pathFilters)
	assert.True(t, result)
}

// fakeTapStream is a minimal tunnel.Stream that records everything sent to it.
type fakeTapStream struct {
	mu   sync.Mutex
	sent [][]byte
}

func (s *fakeTapStream) Tag() tunnel.Tag   { return tunnel.AgentToClient }
func (s *fakeTapStream) ID() tunnel.ConnID { return "" }
func (s *fakeTapStream) Receive(context.Context) (tunnel.Message, error) {
	return nil, io.EOF
}

func (s *fakeTapStream) Send(_ context.Context, m tunnel.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, append([]byte(nil), m.Payload()...))
	return nil
}

func (s *fakeTapStream) CloseSend(context.Context) error { return nil }
func (s *fakeTapStream) PeerVersion() uint16             { return tunnel.Version }
func (s *fakeTapStream) SessionID() tunnel.SessionID     { return "" }
func (s *fakeTapStream) DialTimeout() time.Duration      { return 0 }
func (s *fakeTapStream) RoundtripLatency() time.Duration { return 0 }
func (s *fakeTapStream) SetTag(tunnel.Tag)               {}

func (s *fakeTapStream) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []byte
	for _, b := range s.sent {
		all = append(all, b...)
	}
	return all
}

// fakeStreamProvider always hands out the same stream, regardless of the requested intercept.
type fakeStreamProvider struct {
	stream tunnel.Stream
}

func (p *fakeStreamProvider) CreateClientStream(
	context.Context, tunnel.Tag, tunnel.SessionID, tunnel.ConnID, time.Duration, time.Duration,
) (tunnel.Stream, error) {
	return p.stream, nil
}

func (p *fakeStreamProvider) ReportMetrics(context.Context, *manager.TunnelMetrics) {}
func (p *fakeStreamProvider) MetricsEnabled() bool                                  { return false }

// TestHandleHTTPRequest_WiretapMatch_ForwardsAndTaps proves that a bodied request matching an
// HTTP-filtered wiretap both reaches the application (through defaultHandler) and is streamed to
// the tap as its body is read. The tap goroutine is detached from handleHTTPRequest, so its
// delivery is asserted with a bounded wait rather than immediately after the handler returns.
func TestHandleHTTPRequest_WiretapMatch_ForwardsAndTaps(t *testing.T) {
	ctx := context.Background()
	stream := &fakeTapStream{}
	f := &tcp{interceptor: &interceptor{
		lCtx:           ctx,
		intercepts:     make(interceptControllerMap),
		wiretaps:       make(interceptControllerMap),
		streamProvider: &fakeStreamProvider{stream: stream},
	}}

	wt := &manager.InterceptInfo{
		Id: "wt-1",
		Spec: &manager.InterceptSpec{
			Wiretap:       true,
			TargetHost:    "127.0.0.1",
			TargetPort:    8080,
			HeaderFilters: map[string]string{"k": "v"},
		},
		ClientSession: &manager.SessionInfo{SessionId: "sess-1"},
	}
	f.wiretaps.reconcile(ctx, []*manager.InterceptInfo{wt})

	const body = "hello wiretap"
	var handlerCalled atomic.Bool
	defaultHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled.Store(true)
		b, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, body, string(b))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("app-response"))
	})

	req := httptest.NewRequest(http.MethodPost, "http://example.com/path", strings.NewReader(body))
	req.Header.Set("k", "v")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		f.handleHTTPRequest(rec, req, defaultHandler)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleHTTPRequest deadlocked: request never reached the application")
	}

	require.True(t, handlerCalled.Load(), "matched request was never forwarded to the application")
	require.Equal(t, "app-response", rec.Body.String())

	// The tap is served by a detached goroutine that is intentionally not awaited by
	// handleHTTPRequest, so it may finish slightly after the handler returns.
	require.Eventually(t, func() bool {
		tapped := string(stream.bytes())
		return strings.Contains(tapped, "POST /path") && strings.Contains(tapped, body)
	}, 2*time.Second, 10*time.Millisecond, "tap did not receive the request line and body")
}

// TestHandleHTTPRequest_WiretapMatch_BodylessRequest_ForwardsAndTaps is a regression test for
// the live deadlock this package's tap-request code had: a bodyless GET is never read by
// httputil.ReverseProxy (or any handler that has no reason to touch the body), so nothing ever
// drives the tapped request body to EOF. handleHTTPRequest used to gate its return on a
// defer wg.Wait() for the tap goroutines, and the tap goroutines in turn blocked forever reading
// a pipe that would only ever see EOF once the body was read - so the handler itself hung and
// the caller received nothing. handleHTTPRequest must return once the request has been served,
// regardless of whether anything read the body, while the tap still receives what could be
// captured (the request line and headers).
//
// The defaultHandler below intentionally never reads r.Body, modeling a bodyless GET forwarded
// through httputil.ReverseProxy. Without the fix this test times out instead of failing fast,
// which is why it uses a done-channel with a bounded wait rather than letting a hang block
// `go test` indefinitely.
func TestHandleHTTPRequest_WiretapMatch_BodylessRequest_ForwardsAndTaps(t *testing.T) {
	ctx := context.Background()
	stream := &fakeTapStream{}
	f := &tcp{interceptor: &interceptor{
		lCtx:           ctx,
		intercepts:     make(interceptControllerMap),
		wiretaps:       make(interceptControllerMap),
		streamProvider: &fakeStreamProvider{stream: stream},
	}}

	wt := &manager.InterceptInfo{
		Id: "wt-1",
		Spec: &manager.InterceptSpec{
			Wiretap:       true,
			TargetHost:    "127.0.0.1",
			TargetPort:    8080,
			HeaderFilters: map[string]string{"k": "v"},
		},
		ClientSession: &manager.SessionInfo{SessionId: "sess-1"},
	}
	f.wiretaps.reconcile(ctx, []*manager.InterceptInfo{wt})

	var handlerCalled atomic.Bool
	// Models a reverse proxy forwarding a bodyless GET: it never reads r.Body.
	defaultHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled.Store(true)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("app-response"))
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/path", nil)
	req.Header.Set("k", "v")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		f.handleHTTPRequest(rec, req, defaultHandler)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleHTTPRequest deadlocked: request never returned for a bodyless (GET) request")
	}

	require.True(t, handlerCalled.Load(), "matched request was never forwarded to the application")
	require.Equal(t, "app-response", rec.Body.String())

	require.Eventually(t, func() bool {
		return strings.Contains(string(stream.bytes()), "GET /path")
	}, 2*time.Second, 10*time.Millisecond, "tap did not receive the request line")
}

// TestHandleHTTPRequest_WiretapNoMatch_ForwardsWithoutTapping verifies that a request which
// does not match any wiretap filter is forwarded to the application without being tapped.
func TestHandleHTTPRequest_WiretapNoMatch_ForwardsWithoutTapping(t *testing.T) {
	ctx := context.Background()
	stream := &fakeTapStream{}
	f := &tcp{interceptor: &interceptor{
		lCtx:           ctx,
		intercepts:     make(interceptControllerMap),
		wiretaps:       make(interceptControllerMap),
		streamProvider: &fakeStreamProvider{stream: stream},
	}}

	wt := &manager.InterceptInfo{
		Id: "wt-1",
		Spec: &manager.InterceptSpec{
			Wiretap:       true,
			TargetHost:    "127.0.0.1",
			TargetPort:    8080,
			HeaderFilters: map[string]string{"k": "v"},
		},
		ClientSession: &manager.SessionInfo{SessionId: "sess-1"},
	}
	f.wiretaps.reconcile(ctx, []*manager.InterceptInfo{wt})

	var handlerCalled atomic.Bool
	defaultHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/path", nil)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		f.handleHTTPRequest(rec, req, defaultHandler)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleHTTPRequest deadlocked")
	}

	require.True(t, handlerCalled.Load(), "non-matching request was never forwarded to the application")
	require.Empty(t, stream.bytes(), "tap should not have received any data for a non-matching request")
}
