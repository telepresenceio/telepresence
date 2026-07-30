package dns

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// lookupPollTimeout/lookupPollInterval bound how long a DNS resolution check
// (success or failure) is given to converge: right after a connect, or after
// a kubeconfig extension's excludes/mappings takes effect, the daemon's
// resolver may briefly still reflect the prior state. lookupProbeTimeout
// bounds each individual lookup attempt within that poll.
const (
	lookupPollTimeout  = 10 * time.Second
	lookupPollInterval = time.Second
	lookupProbeTimeout = 2 * time.Second
)

// lookupSucceeds reports whether host currently resolves from the test
// process: the same net.DefaultResolver.LookupHost call
// integration_test/subdomain_test.go's lookupHost and
// integration_test/svcdomain_test.go's Test_SvcDomain use to prove a cluster
// name resolves while connected.
func lookupSucceeds(ctx context.Context, host string) bool {
	lctx, cancel := context.WithTimeout(ctx, lookupProbeTimeout)
	defer cancel()
	_, err := net.DefaultResolver.LookupHost(lctx, host)
	return err == nil
}

// freeDefaultConnection claims the shared default connection fixture (so the
// engine re-provisions it fresh afterwards) and quits whatever daemon is
// running under that identity. A test about to connect under a derived
// kubeconfig (dns.excludes/dns.mappings) must call this first: the host
// (non-docker) connector daemon is a singleton, so switching KUBECONFIG out
// from under an already-memoized default connection would otherwise leave
// that fixture's *Conn pointed at a dead daemon. Mirrors
// suites/connect/lifecycle.go's and suites/namespaces/helpers.go's
// identically named helper (private to those packages, so duplicated here).
func freeDefaultConnection(t *testing.T, ns string) {
	t.Helper()
	conn := rt.Mutate(t, rt.ConnectionFixture(ns))
	conn.Disconnect(t)
}
