package manager

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"

	"google.golang.org/grpc"

	"github.com/datawire/dlib/dlog"
	systemarpc "github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/systema"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
)

type ReverseConnProvider struct {
	mgr *Manager
}

type ReverseConnClient struct {
	systemarpc.SystemACRUDClient
	wait func() error
}

func (p *ReverseConnProvider) GetSystemaAddress(ctx context.Context) (string, error) {
	env := managerutil.GetEnv(p.mgr.ctx)
	return net.JoinHostPort(env.SystemAHost, env.SystemAPort), nil
}

func (p *ReverseConnProvider) GetAPIKey(ctx context.Context) (string, error) {
	sessionID := managerutil.GetSessionID(ctx)
	var apikey string
	if sessionID != "" {
		client := p.mgr.state.GetClient(sessionID)
		apikey = client.GetApiKey()
	} else {
		// Uhh... pick one arbitrarily.  This case should be limited to the
		// ReverseConnection call, since that call doesn't belong to any one user action.
		// This can also happen if RemoveIntercept + RemoveDomain is called when a user
		// quits a session and the manager reaps intercepts + the domain itself.
		for _, client := range p.mgr.state.GetAllClients() {
			if client.ApiKey != "" {
				apikey = client.ApiKey
				break
			}
		}

		// If there were no other clients using telepresence, we try to find an APIKey
		// used for creating an intercept.
		if apikey == "" {
			apikey = p.mgr.state.GetInterceptAPIKey()
		}
	}
	if apikey == "" {
		return "", errors.New("no apikey has been provided by a client")
	}
	return apikey, nil
}

func (p *ReverseConnProvider) GetInstallID(ctx context.Context) (string, error) {
	return "", nil
}

func (p *ReverseConnProvider) GetExtraHeaders(ctx context.Context) (map[string]string, error) {
	return map[string]string{
		"X-Telepresence-ManagerID": p.mgr.ID,
	}, nil
}

func (p *ReverseConnProvider) BuildClient(ctx context.Context, conn *grpc.ClientConn) (*ReverseConnClient, error) {
	client, wait, err := systema.ConnectToSystemA(ctx, p.mgr, conn)
	if err != nil {
		return nil, err
	}
	return &ReverseConnClient{client, wait}, nil
}

func (c *ReverseConnClient) Close(ctx context.Context) error {
	return c.wait()
}

func (m *Manager) DialIntercept(ctx context.Context, interceptID string) (net.Conn, error) {
	intercept, _ := m.state.GetIntercept(interceptID)
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
