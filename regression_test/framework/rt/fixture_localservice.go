package rt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
)

// localMarkerPrefix begins the body every LocalService responds with; the
// full marker also appears in RoutedToCluster's negative check.
const localMarkerPrefix = "rtest-local:"

// maxObservedRequests caps LocalService's request log: enough to prove a
// wiretap copy (or any other) request arrived without growing unbounded
// over a long-lived local service.
const maxObservedRequests = 50

// LocalService is an in-process HTTP server bound to 127.0.0.1:0. A GET (or
// any other method carrying no body) gets a response body carrying a marker
// unique to this instance, which RoutedToLocal asserts on; a PUT or POST
// gets its request body echoed back verbatim instead. It is never memoized
// or adopted: every call to Suite.LocalEcho starts a fresh listener.
type LocalService struct {
	id       string
	listener net.Listener
	srv      *http.Server
	marker   string

	reqMu    sync.Mutex
	requests []string // "METHOD path", oldest first, capped at maxObservedRequests
}

// newLocalService starts a LocalService and registers its shutdown as a
// test cleanup.
func newLocalService(t testing.TB) *LocalService {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("LocalEcho: listen: %v", err)
	}
	ls := &LocalService{
		id:       randomID(),
		listener: l,
	}
	ls.marker = localMarkerPrefix + ls.id
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		ls.recordRequest(r)
		if r.Method == http.MethodPut || r.Method == http.MethodPost {
			_, _ = io.Copy(w, r.Body)
			return
		}
		_, _ = w.Write([]byte(ls.marker))
	})
	ls.srv = &http.Server{Handler: mux}
	go func() { _ = ls.srv.Serve(l) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ls.srv.Shutdown(ctx)
	})
	return ls
}

// recordRequest appends r's method and path to the request log, dropping
// the oldest entry once maxObservedRequests is reached.
func (ls *LocalService) recordRequest(r *http.Request) {
	ls.reqMu.Lock()
	defer ls.reqMu.Unlock()
	if len(ls.requests) >= maxObservedRequests {
		ls.requests = ls.requests[1:]
	}
	ls.requests = append(ls.requests, r.Method+" "+r.URL.Path)
}

// Requests returns the "METHOD path" of every request this service has
// received since it started (or since the last ResetRequests), oldest
// first, capped at maxObservedRequests. Used to observe traffic a wiretap
// copies to this service, which RoutedToLocal/RoutedToCluster's status/body
// assertions can't: a wiretap is a passive copy, not something the request
// that triggered it waits on.
func (ls *LocalService) Requests() []string {
	ls.reqMu.Lock()
	defer ls.reqMu.Unlock()
	out := make([]string, len(ls.requests))
	copy(out, ls.requests)
	return out
}

// ResetRequests clears the request log.
func (ls *LocalService) ResetRequests() {
	ls.reqMu.Lock()
	defer ls.reqMu.Unlock()
	ls.requests = nil
}

func randomID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Port returns the local TCP port the service is bound to.
func (ls *LocalService) Port() int {
	return ls.listener.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // always tcp, see net.Listen above
}

// Marker returns the response body this service always returns.
func (ls *LocalService) Marker() string {
	return ls.marker
}

// ToLocal builds an InterceptOpt that routes the intercept/ingest to ls's
// local port, matching remote (a service port name or number, as required
// by cli.Port).
func ToLocal(ls *LocalService, remote string) cli.InterceptOpt {
	return cli.Port(ls.Port(), remote)
}
