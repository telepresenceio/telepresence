package portforward

// Regression tests for issue #4222: one consumer of the shared per-pod
// stream connection must not be able to corrupt it for its sibling stream
// pairs.
//
// The server side replicates the real chain that terminates a cluster's
// port-forward: the SPDY response upgrader from k8s.io/streaming (the same
// code kubelet and containerd use via k8s staging modules), the kubelet
// httpStreamHandler (k8s.io/kubelet/pkg/cri/streaming/portforward, v1.35.0)
// copied verbatim minus logging, and containerd's criService.portForward
// copy loops (v2.2.0) including the 1-second grace after the first copy
// direction ends.
//
// The client side dials with the k8s.io/streaming SPDY round tripper, whose
// streams are native *spdystream.Stream values implementing net.Conn — the
// same shape the WebSocket-tunneled transport produces. This is the shape
// where a deadline forwarded by portConn reaches the shared underlying
// connection.

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime/pprof"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	"k8s.io/streaming/pkg/httpstream"
	streamspdy "k8s.io/streaming/pkg/httpstream/spdy"
)

// testLog wraps testing.T logging so that harness goroutines that outlive
// the test (containerd's 1s grace, copy loops unblocked at teardown) do not
// log after the test has completed.
type testLog struct {
	mu   sync.Mutex
	t    *testing.T
	done bool
}

func newTestLog(t *testing.T) *testLog {
	l := &testLog{t: t}
	t.Cleanup(func() {
		l.mu.Lock()
		l.done = true
		l.mu.Unlock()
	})
	return l
}

func (l *testLog) Logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.done {
		l.t.Logf(format, args...)
	}
}

func (l *testLog) Log(args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.done {
		l.t.Log(args...)
	}
}

// containerdForward mimics containerd v2.2.0 criService.portForward: two copy
// goroutines, return when the first direction ends, give the second direction
// one second, then close both ends.
func containerdForward(log *testLog, target string) func(stream io.ReadWriteCloser) error {
	return func(stream io.ReadWriteCloser) error {
		defer stream.Close()
		conn, err := net.Dial("tcp", target)
		if err != nil {
			return err
		}
		defer conn.Close()

		errCh := make(chan error, 2)
		go func() {
			_, err := io.Copy(stream, conn)
			log.Logf("server: pod->client copy ended: %v", err)
			errCh <- err
		}()
		go func() {
			_, err := io.Copy(conn, stream)
			log.Logf("server: client->pod copy ended: %v", err)
			errCh <- err
		}()

		errFwd := <-errCh
		select {
		case e := <-errCh:
			if errFwd == nil {
				errFwd = e
			}
		case <-time.After(time.Second):
			log.Log("server: 1s grace expired with one copy direction still running")
		}
		return errFwd
	}
}

// testStreamPair is kubelet's httpStreamPair.
type testStreamPair struct {
	lock        sync.RWMutex
	requestID   string
	dataStream  httpstream.Stream
	errorStream httpstream.Stream
	complete    chan struct{}
}

func (p *testStreamPair) add(stream httpstream.Stream) (bool, error) {
	p.lock.Lock()
	defer p.lock.Unlock()
	switch stream.Headers().Get(core.StreamType) {
	case core.StreamTypeError:
		if p.errorStream != nil {
			return false, errors.New("error stream already assigned")
		}
		p.errorStream = stream
	case core.StreamTypeData:
		if p.dataStream != nil {
			return false, errors.New("data stream already assigned")
		}
		p.dataStream = stream
	}
	complete := p.errorStream != nil && p.dataStream != nil
	if complete {
		close(p.complete)
	}
	return complete, nil
}

func (p *testStreamPair) printError(s string) {
	p.lock.RLock()
	defer p.lock.RUnlock()
	if p.errorStream != nil {
		_, _ = fmt.Fprint(p.errorStream, s)
	}
}

// testStreamHandler is kubelet's httpStreamHandler (v1.35.0), minus logging.
type testStreamHandler struct {
	log                   *testLog
	conn                  httpstream.Connection
	streamChan            chan httpstream.Stream
	streamPairsLock       sync.RWMutex
	streamPairs           map[string]*testStreamPair
	streamCreationTimeout time.Duration
	forwarder             func(port int32, stream io.ReadWriteCloser) error
}

func (h *testStreamHandler) getStreamPair(requestID string) (*testStreamPair, bool) {
	h.streamPairsLock.Lock()
	defer h.streamPairsLock.Unlock()
	if p, ok := h.streamPairs[requestID]; ok {
		return p, false
	}
	p := &testStreamPair{requestID: requestID, complete: make(chan struct{})}
	h.streamPairs[requestID] = p
	return p, true
}

func (h *testStreamHandler) monitorStreamPair(p *testStreamPair, timeout <-chan time.Time) {
	select {
	case <-timeout:
		p.printError(fmt.Sprintf("(request=%s) timed out waiting for streams", p.requestID))
	case <-p.complete:
	}
	h.removeStreamPair(p.requestID)
}

func (h *testStreamHandler) removeStreamPair(requestID string) {
	h.streamPairsLock.Lock()
	defer h.streamPairsLock.Unlock()
	if h.conn != nil {
		pair := h.streamPairs[requestID]
		h.conn.RemoveStreams(pair.dataStream, pair.errorStream)
	}
	delete(h.streamPairs, requestID)
}

func (h *testStreamHandler) requestID(stream httpstream.Stream) string {
	requestID := stream.Headers().Get(core.PortForwardRequestIDHeader)
	if len(requestID) == 0 {
		streamType := stream.Headers().Get(core.StreamType)
		switch streamType {
		case core.StreamTypeError:
			requestID = strconv.Itoa(int(stream.Identifier()))
		case core.StreamTypeData:
			requestID = strconv.Itoa(int(stream.Identifier()) - 2)
		}
	}
	return requestID
}

func (h *testStreamHandler) run() {
	for {
		select {
		case <-h.conn.CloseChan():
			h.log.Log("server: stream connection closed, handler loop exiting")
			return
		case stream := <-h.streamChan:
			requestID := h.requestID(stream)
			p, created := h.getStreamPair(requestID)
			if created {
				go h.monitorStreamPair(p, time.After(h.streamCreationTimeout))
			}
			if complete, err := p.add(stream); err != nil {
				p.printError(fmt.Sprintf("error processing stream for request %s: %v", requestID, err))
			} else if complete {
				go h.portForward(p)
			}
		}
	}
}

func (h *testStreamHandler) portForward(p *testStreamPair) {
	defer p.dataStream.Close()
	defer p.errorStream.Close()

	portString := p.dataStream.Headers().Get(core.PortHeader)
	port, _ := strconv.ParseInt(portString, 10, 32)

	err := h.forwarder(int32(port), p.dataStream)
	h.log.Logf("server: forwarder for request %s port %d returned: %v", p.requestID, port, err)
	if err != nil {
		msg := fmt.Errorf("error forwarding port %d: %v", port, err)
		_, _ = fmt.Fprint(p.errorStream, msg.Error())
		h.log.Logf("server: wrote error to error stream and resetting streams for request %s", p.requestID)
		p.dataStream.Reset()  //nolint:errcheck // kubelet ignores this too
		p.errorStream.Reset() //nolint:errcheck // kubelet ignores this too
	}
}

func streamReceived(streams chan httpstream.Stream) func(httpstream.Stream, <-chan struct{}) error {
	return func(stream httpstream.Stream, replySent <-chan struct{}) error {
		portString := stream.Headers().Get(core.PortHeader)
		if len(portString) == 0 {
			return fmt.Errorf("%q header is required", core.PortHeader)
		}
		if _, err := strconv.ParseUint(portString, 10, 16); err != nil {
			return fmt.Errorf("unable to parse %q as a port: %v", portString, err)
		}
		streamType := stream.Headers().Get(core.StreamType)
		if streamType != core.StreamTypeError && streamType != core.StreamTypeData {
			return fmt.Errorf("invalid stream type %q", streamType)
		}
		streams <- stream
		return nil
	}
}

// startPortForwardServer starts an HTTP server that upgrades to SPDY and
// forwards each stream pair to the target registered for its port, exactly as
// a kubelet/containerd chain would.
func startPortForwardServer(t *testing.T, targets map[int32]string) *httptest.Server {
	log := newTestLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, err := httpstream.Handshake(req, w, []string{ProtocolV1Name})
		if err != nil {
			t.Errorf("handshake: %v", err)
			return
		}
		streamChan := make(chan httpstream.Stream, 1)
		upgrader := streamspdy.NewResponseUpgrader()
		conn := upgrader.UpgradeResponse(w, req, streamReceived(streamChan))
		if conn == nil {
			t.Error("unable to upgrade connection")
			return
		}
		defer conn.Close()
		conn.SetIdleTimeout(4 * time.Hour)
		h := &testStreamHandler{
			log:                   log,
			conn:                  conn,
			streamChan:            streamChan,
			streamPairs:           make(map[string]*testStreamPair),
			streamCreationTimeout: 30 * time.Second,
			forwarder: func(port int32, stream io.ReadWriteCloser) error {
				target, ok := targets[port]
				if !ok {
					return fmt.Errorf("no target for port %d", port)
				}
				return containerdForward(log, target)(stream)
			},
		}
		h.run()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// startEcho starts a TCP echo server standing in for a long-lived pod port
// (the manager's gRPC port).
func startEcho(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(c, c)
				_ = c.Close()
			}()
		}
	}()
	return l.Addr().String()
}

// startOneShot starts a TCP server standing in for a one-shot exchange pod
// port (the manager's x509 auth listener): read a request, write a response,
// close.
func startOneShot(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				buf := make([]byte, 64)
				n, err := c.Read(buf)
				if err == nil {
					_, _ = c.Write([]byte("reply:" + string(buf[:n])))
				}
				_ = c.Close()
			}()
		}
	}()
	return l.Addr().String()
}

// step runs f with a watchdog; on timeout it dumps all goroutines and fails.
func step(t *testing.T, name string, timeout time.Duration, f func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		require.NoError(t, err, name)
		t.Logf("step %s: ok", name)
	case <-time.After(timeout):
		t.Logf("step %s: TIMED OUT, goroutine dump follows", name)
		_ = pprof.Lookup("goroutine").WriteTo(testWriter{t}, 2)
		t.Fatalf("step %s timed out after %s", name, timeout)
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

// dialTestPod upgrades a connection to the test server with the
// k8s.io/streaming SPDY round tripper and wraps it in a podDialer.
func dialTestPod(t *testing.T, serverURL string) *podDialer {
	rt, err := streamspdy.NewRoundTripper(nil)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, serverURL, nil)
	require.NoError(t, err)
	req.Header.Add(httpstream.HeaderProtocolVersion, ProtocolV1Name)
	resp, err := (&http.Client{Transport: rt}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	streamConn, err := rt.NewConnection(resp)
	require.NoError(t, err)
	require.Equal(t, ProtocolV1Name, resp.Header.Get(httpstream.HeaderProtocolVersion))
	t.Cleanup(func() { _ = streamConn.Close() })
	log := newTestLog(t)
	return &podDialer{streamConn: streamConn, onClose: func() { log.Log("podDialer onClose (evicted)") }}
}

func roundtrip(conn net.Conn, payload string) error {
	if _, err := conn.Write([]byte(payload)); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if n == 0 {
		return errors.New("empty reply")
	}
	return nil
}

// sharedConnFixture opens a long-lived pair (standing in for the gRPC
// channel, with a continuous reader like a real h2 transport) plus a
// short-lived pair against a one-shot target, on one shared stream
// connection. It returns the podDialer, the short-lived conn, and a ping
// function that does a write/read roundtrip on the long-lived pair.
func sharedConnFixture(t *testing.T) (pd *podDialer, short net.Conn, longPing func(string) error) {
	ctx := t.Context()
	echo := startEcho(t)
	oneShot := startOneShot(t)
	srv := startPortForwardServer(t, map[int32]string{2222: echo, 1111: oneShot})
	pd = dialTestPod(t, srv.URL)

	var long net.Conn
	step(t, "dial long-lived", 10*time.Second, func() (err error) {
		long, err = pd.dial(ctx, 2222)
		return err
	})

	log := newTestLog(t)
	replies := make(chan []byte, 100)
	go func() {
		for {
			buf := make([]byte, 256)
			n, err := long.Read(buf)
			if err != nil {
				log.Logf("client: long-lived reader ended: %v", err)
				close(replies)
				return
			}
			replies <- buf[:n]
		}
	}()
	longPing = func(payload string) error {
		if _, err := long.Write([]byte(payload)); err != nil {
			return fmt.Errorf("write: %w", err)
		}
		select {
		case r, ok := <-replies:
			if !ok {
				return errors.New("long-lived reader is gone")
			}
			log.Logf("client: long-lived reply: %q", string(r))
			return nil
		case <-time.After(5 * time.Second):
			return errors.New("no reply within 5s")
		}
	}

	step(t, "roundtrip long-lived", 10*time.Second, func() error {
		return longPing("ping-baseline")
	})

	step(t, "dial short-lived", 10*time.Second, func() (err error) {
		short, err = pd.dial(ctx, 1111)
		return err
	})
	step(t, "exchange short-lived", 10*time.Second, func() error {
		// The one-shot target replies and immediately closes its socket.
		return roundtrip(short, "handshake")
	})
	return pd, short, longPing
}

// assertSiblingsHealthy verifies that the long-lived pair still works and
// that a new pair can be dialed on the shared connection.
func assertSiblingsHealthy(t *testing.T, pd *podDialer, longPing func(string) error) {
	for i := range 3 {
		step(t, fmt.Sprintf("roundtrip long-lived after close #%d", i), 15*time.Second, func() error {
			return longPing(fmt.Sprintf("ping-post-%d", i))
		})
	}
	var late net.Conn
	step(t, "dial late pair", 15*time.Second, func() (err error) {
		late, err = pd.dial(t.Context(), 2222)
		return err
	})
	step(t, "roundtrip late pair", 10*time.Second, func() error {
		return roundtrip(late, "ping-late")
	})
}

// Test_PairCloseKeepsSiblingsHealthy closes a short-lived stream pair,
// including a trailing write into the already-closed pod socket, while a
// long-lived pair stays active on the same shared connection.
func Test_PairCloseKeepsSiblingsHealthy(t *testing.T) {
	pd, short, longPing := sharedConnFixture(t)

	step(t, "close short-lived with trailing payload", 10*time.Second, func() error {
		if _, err := short.Write([]byte("trailing")); err != nil {
			t.Logf("client: trailing write error (acceptable): %v", err)
		}
		return short.Close()
	})

	assertSiblingsHealthy(t, pd, longPing)
}

// Test_PairDeadlineDoesNotPoisonSiblings performs, on the short-lived pair,
// the exact deadline sequence crypto/tls.Conn.Close runs against its
// underlying conn (closeNotify: a 5s write deadline, the close_notify write,
// then a write deadline in the past), and then closes the pair. The
// spdystream layer has no per-stream deadlines — a forwarded deadline lands
// on the shared connection and breaks every sibling's writes with i/o
// timeouts (issue #4222). portConn must swallow deadline calls instead.
func Test_PairDeadlineDoesNotPoisonSiblings(t *testing.T) {
	pd, short, longPing := sharedConnFixture(t)

	step(t, "close short-lived the way crypto/tls does", 10*time.Second, func() error {
		require.NoError(t, short.SetWriteDeadline(time.Now().Add(5*time.Second)))
		if _, err := short.Write([]byte("close-notify")); err != nil {
			t.Logf("client: close_notify write error (acceptable): %v", err)
		}
		require.NoError(t, short.SetWriteDeadline(time.Now()))
		return short.Close()
	})

	assertSiblingsHealthy(t, pd, longPing)
}
