package agent

import (
	"context"
	"sync"

	"golang.org/x/oauth2"
	"google.golang.org/grpc/credentials"
	"k8s.io/client-go/transport"

	"github.com/telepresenceio/clog"
)

// managerTokenCredentials is a credentials.PerRPCCredentials that attaches the
// traffic-agent's projected manager-audience ServiceAccount token as a bearer
// credential on every RPC to the traffic-manager. The token is read from the
// file on demand through a caching source, so rotation of the projected volume
// is picked up without reconnecting.
type managerTokenCredentials struct {
	source   oauth2.TokenSource
	warnOnce sync.Once
}

var _ credentials.PerRPCCredentials = (*managerTokenCredentials)(nil)

// newManagerTokenCredentials returns credentials backed by the manager-audience
// token file at path.
func newManagerTokenCredentials(path string) *managerTokenCredentials {
	return &managerTokenCredentials{source: transport.NewCachedFileTokenSource(path)}
}

// GetRequestMetadata returns the bearer authorization header for the current
// token. A read failure is logged once and yields empty metadata rather than
// an error, so a transient problem reading the rotating token file never
// fails every agent RPC; the manager treats a missing token according to its
// own enforcement mode. Token contents are never logged.
func (c *managerTokenCredentials) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	tok, err := c.source.Token()
	if err != nil {
		c.warnOnce.Do(func() {
			clog.Warnf(ctx, "unable to read manager token: %v", err)
		})
		return map[string]string{}, nil
	}
	return map[string]string{"authorization": "Bearer " + tok.AccessToken}, nil
}

// RequireTransportSecurity is false: the connection to the traffic-manager is
// plaintext h2c inside the cluster network, and the token itself is audience-
// bound to the manager so it carries no value if intercepted outside it.
func (c *managerTokenCredentials) RequireTransportSecurity() bool {
	return false
}
