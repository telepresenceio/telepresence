package rt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
)

// localMarkerPrefix begins the body every LocalService responds with; the
// full marker also appears in RoutedToCluster's negative check.
const localMarkerPrefix = "rtest-local:"

// LocalService is an in-process HTTP server bound to 127.0.0.1:0. It
// responds to every request with a body carrying a marker unique to this
// instance, which RoutedToLocal asserts on. It is never memoized or
// adopted: every call to Suite.LocalEcho starts a fresh listener.
type LocalService struct {
	id       string
	listener net.Listener
	srv      *http.Server
	marker   string
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
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
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
