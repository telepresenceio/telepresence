package fwd

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func TestTCPDispatch_HTTPMechanism_Handled(t *testing.T) {
	// Create a minimal tcp interceptor instance
	f := &tcp{interceptor: &interceptor{
		lCtx:       context.Background(),
		intercepts: make(interceptControllerMap),
		wiretaps:   make(interceptControllerMap),
	}}

	// net.Pipe gives us a pair of in-memory connections
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	// Close the server side immediately to cause EOF on read
	serverConn.Close()

	// Minimal intercept info with HTTP filters (enables HTTP mechanism)
	intercept := &manager.InterceptInfo{Spec: &manager.InterceptSpec{
		HeaderFilters: map[string]string{"X-Test": "value"},
	}}

	// Set the HTTP intercepts (simulates what fwdstate.HandlePort does)
	f.SetIntercepting([]*manager.InterceptInfo{intercept})

	// Call dispatch. Since the connection is closed, the HTTP handler
	// will get EOF when trying to read and return an error.
	handled := f.IsHTTP()
	require.True(t, handled, "expected HTTP mechanism to be handled by TCP dispatch")
}

func TestTCPDispatch_NoMechanism_NotHandled(t *testing.T) {
	f := &tcp{interceptor: &interceptor{
		lCtx:       context.Background(),
		intercepts: make(interceptControllerMap),
		wiretaps:   make(interceptControllerMap),
	}}

	// No intercept
	handled := f.IsHTTP()
	require.False(t, handled)

	// Intercept without HTTP mechanism (no filters)
	intercept := &manager.InterceptInfo{Spec: &manager.InterceptSpec{}}
	f.SetIntercepting([]*manager.InterceptInfo{intercept})
	handled = f.IsHTTP()
	require.False(t, handled)
}

func TestPassThroughConnectionReconnectsForFilteredIntercept(t *testing.T) {
	shared := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "shared")
	}))
	defer shared.Close()
	target := netip.MustParseAddrPort(strings.TrimPrefix(shared.URL, "http://"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := NewTCPInterceptor(ctx, types.PortAndProto{Proto: types.ProtoTCP},
		tunnel.AgentToClient, nil, target).(*tcp)
	ready := make(chan netip.AddrPort)
	serveDone := make(chan error, 1)
	go func() { serveDone <- f.Serve(ctx, ready) }()
	var proxyAddr netip.AddrPort
	select {
	case proxyAddr = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("agent listener did not start")
	}

	transport := &http.Transport{MaxIdleConnsPerHost: 1}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	url := "http://" + proxyAddr.String() + "/ready"
	request := func(filtered bool) (int, string, bool) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, url, nil)
		require.NoError(t, err)
		if filtered {
			req.Header.Set("X-Test", "intercept")
		}
		reused := false
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
		}))
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(body), reused
	}

	status, body, reused := request(false)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "shared", body)
	require.False(t, reused)

	status, body, reused = request(false)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "shared", body)
	require.True(t, reused, "normal traffic must keep its pass-through connection")

	f.SetStreamProvider(&fakeStreamProvider{stream: &fakeTapStream{}})
	f.SetIntercepting([]*manager.InterceptInfo{{
		Id:            "filtered",
		ClientSession: &manager.SessionInfo{SessionId: "client"},
		Spec: &manager.InterceptSpec{
			Name:          "filtered",
			TargetHost:    target.Addr().String(),
			TargetPort:    int32(target.Port()),
			HeaderFilters: map[string]string{"X-Test": "intercept"},
		},
	}})
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.passThrough) == 0
	}, time.Second, time.Millisecond, "activation must close the idle pass-through connection")
	status, body, reused = request(true)
	require.False(t, reused, "request must reconnect after filtered intercept activation")
	require.NotEqual(t, "shared", body, "filtered request bypassed the HTTP handler")
	require.NotEqual(t, http.StatusOK, status)

	status, body, reused = request(false)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "shared", body, "nonmatching request must reach the application")
	require.True(t, reused, "HTTP routing must preserve the new keep-alive connection")

	f.setListenerSwitch()
	status, body, reused = request(false)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "shared", body)
	require.True(t, reused, "an HTTP listener update must not close HTTP connections")

	cancel()
	select {
	case err := <-serveDone:
		require.ErrorIs(t, err, net.ErrClosed)
	case <-time.After(5 * time.Second):
		t.Fatal("agent listener did not stop")
	}
}

func TestTCPForwardAfterHTTPActivationClosesConnection(t *testing.T) {
	for _, wiretap := range []bool{false, true} {
		t.Run(map[bool]string{false: "intercept", true: "wiretap"}[wiretap], func(t *testing.T) {
			ctx := context.Background()
			f := NewTCPInterceptor(ctx, types.PortAndProto{Proto: types.ProtoTCP},
				tunnel.AgentToClient, nil, netip.MustParseAddrPort("127.0.0.1:8080"))
			ii := &manager.InterceptInfo{Spec: &manager.InterceptSpec{
				HeaderFilters: map[string]string{"X-Test": "intercept"},
				Wiretap:       wiretap,
			}}
			if wiretap {
				f.SetWiretapping([]*manager.InterceptInfo{ii})
			} else {
				f.SetIntercepting([]*manager.InterceptInfo{ii})
			}
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			require.NoError(t, client.SetReadDeadline(time.Now().Add(time.Second)))
			require.NoError(t, f.Forward(ctx, server))
			_, err := client.Read(make([]byte, 1))
			require.ErrorIs(t, err, io.EOF)
		})
	}
}

func TestHTTPInterceptTransportReusedAndPruned(t *testing.T) {
	f := NewTCPInterceptor(
		context.Background(),
		types.PortAndProto{Port: 3000, Proto: types.ProtoTCP},
		tunnel.AgentToClient,
		nil,
		netip.MustParseAddrPort("127.0.0.1:3000"),
	).(*tcp)
	ii := &manager.InterceptInfo{
		Id:            "intercept-id",
		ClientSession: &manager.SessionInfo{SessionId: "client-session"},
		Spec: &manager.InterceptSpec{
			TargetHost:    "127.0.0.1",
			TargetPort:    3000,
			HeaderFilters: map[string]string{"X-Test": "value"},
		},
	}

	first := f.getHTTPInterceptTransport(context.Background(), ii, 1)
	second := f.getHTTPInterceptTransport(context.Background(), ii, 1)
	require.Same(t, first, second)

	// HTTP/1 and HTTP/2 requests may need different transport protocol
	// settings, so they must not share the same cached entry.
	require.NotSame(t, first, f.getHTTPInterceptTransport(context.Background(), ii, 2))

	key := httpInterceptTransportKey(ii, 1)
	_, ok := f.httpTransportCache.Load(key)
	require.True(t, ok)

	f.SetIntercepting(nil)
	_, ok = f.httpTransportCache.Load(key)
	require.False(t, ok)
}
