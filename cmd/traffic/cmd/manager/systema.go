package manager

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	systemarpc "github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/systema"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
)

type systemaCredentials struct {
	mgr *Manager
}

// GetRequestMetadata implements credentials.PerRPCCredentials.
func (c *systemaCredentials) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	sessionID := managerutil.GetSessionID(ctx)

	var apikey string
	if sessionID != "" {
		client := c.mgr.state.GetClient(sessionID)
		apikey = client.GetApiKey()
	} else {
		// Uhh... pick one arbitrarily.  This case should be limited to the
		// ReverseConnection call, since that call doesn't belong to any one user action.
		// This can also happen if RemoveIntercept + RemoveDomain is called when a user
		// quits a session and the manager reaps intercepts + the domain itself.
		for _, client := range c.mgr.state.GetAllClients() {
			if client.ApiKey != "" {
				apikey = client.ApiKey
				break
			}
		}

		// If there were no other clients using telepresence, we try to find an APIKey
		// used for creating an intercept.
		if apikey == "" {
			apikey = c.mgr.state.GetInterceptAPIKey()
		}
	}
	if apikey == "" {
		return nil, errors.New("no apikey has been provided by a client")
	}

	md := map[string]string{
		"X-Telepresence-ManagerID": c.mgr.ID,
		"X-Ambassador-Api-Key":     apikey,
	}
	return md, nil
}

// RequireTransportSecurity implements credentials.PerRPCCredentials.
func (c *systemaCredentials) RequireTransportSecurity() bool {
	return true
}

func (m *Manager) DialIntercept(ctx context.Context, interceptID string) (net.Conn, error) {
	intercept := m.state.GetIntercept(interceptID)
	if intercept == nil || intercept.PreviewSpec.Ingress == nil {
		return nil, fmt.Errorf("missing ingress information for intercept %s", interceptID)
	}
	ingressInfo := intercept.PreviewSpec.Ingress

	dialAddr := fmt.Sprintf("%s:%d", ingressInfo.Host, ingressInfo.Port)
	if ingressInfo.UseTls {
		dialer := &tls.Dialer{
			Config: &tls.Config{
				InsecureSkipVerify: true,
				ServerName:         ingressInfo.L5Host,
			},
		}
		dlog.Debugf(ctx, "HandleConnection: dialing intercept %s using TLS on %s", interceptID, dialAddr)
		return dialer.DialContext(ctx, "tcp", dialAddr)
	}

	dialer := net.Dialer{}
	dlog.Debugf(ctx, "HandleConnection: dialing intercept %s using clear text on %s", interceptID, dialAddr)
	return dialer.DialContext(ctx, "tcp", dialAddr)
}

type systemaPool struct {
	mgr *Manager

	mu     sync.Mutex
	count  int64
	ctx    context.Context
	cancel context.CancelFunc
	client systemarpc.SystemACRUDClient
	wait   func() error
}

func NewSystemAPool(mgr *Manager) *systemaPool {
	return &systemaPool{
		mgr: mgr,
	}
}

func (p *systemaPool) Get() (systemarpc.SystemACRUDClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.ctx == nil {
		env := managerutil.GetEnv(p.mgr.ctx)
		host := env.SystemAHost
		port := env.SystemAPort

		ctx, cancel := context.WithCancel(dgroup.WithGoroutineName(p.mgr.ctx, "/systema"))
		client, wait, err := systema.ConnectToSystemA(
			ctx, p.mgr, net.JoinHostPort(host, port),
			grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{ServerName: host})),
			grpc.WithPerRPCCredentials(&systemaCredentials{p.mgr}))
		if err != nil {
			cancel()
			return nil, err
		}
		p.ctx, p.cancel, p.client, p.wait = ctx, cancel, client, wait
	}

	p.count++
	return p.client, nil
}

func (p *systemaPool) Done() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.count--
	var err error
	if p.count == 0 {
		p.cancel()
		err = p.wait()
		p.ctx, p.cancel, p.client, p.wait = nil, nil, nil, nil
	}
	return err
}
