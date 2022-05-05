package a8rcloud

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
)

const (
	ApiKeyHeader           = "X-Ambassador-Api-Key"
	InstallIDHeader        = "X-Ambassador-Install-ID"
	TrafficManagerIDHeader = "X-Telepresence-ManagerID"
)

const (
	TrafficManagerConnName = "traffic-manager"
	UserdConnName          = "userd"
)

type Closeable interface {
	Close(ctx context.Context) error
}

type HeaderProvider interface {
	GetAPIKey(ctx context.Context) (string, error)
	GetInstallID(ctx context.Context) (string, error)
	GetExtraHeaders(ctx context.Context) (map[string]string, error)
}

type ClientProvider[T Closeable] interface {
	HeaderProvider
	GetSystemaAddress(ctx context.Context) (string, error)
	BuildClient(ctx context.Context, conn *grpc.ClientConn) (T, error)
}

type systemaPoolKey string

func WithSystemAPool[T Closeable](ctx context.Context, poolName string, provider ClientProvider[T]) context.Context {
	return context.WithValue(ctx, systemaPoolKey(poolName), &systemAPool[T]{Provider: provider, Name: poolName})
}

func GetSystemAPool[T Closeable](ctx context.Context, poolName string) SystemAPool[T] {
	if p, ok := ctx.Value(systemaPoolKey(poolName)).(*systemAPool[T]); ok {
		return p
	}
	return nil
}

type SystemAPool[T Closeable] interface {
	Get(ctx context.Context) (T, error)
	Done(ctx context.Context) error
}

type systemAPool[T Closeable] struct {
	Provider ClientProvider[T]
	Name     string
	mu       sync.Mutex
	count    int64
	ctx      context.Context
	cancel   context.CancelFunc
	client   T
}

func (p *systemAPool[T]) Get(ctx context.Context) (T, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.ctx == nil {
		ctx, cancel := context.WithCancel(dgroup.WithGoroutineName(ctx, "/systema"))
		// Ya can't return generic nil, but this'll do it
		var client T

		sysaAddr, err := p.Provider.GetSystemaAddress(ctx)
		if err != nil {
			cancel()
			return client, err
		}
		host, _, err := net.SplitHostPort(sysaAddr)
		if err != nil {
			cancel()
			return client, fmt.Errorf("system a address %s is malformed: %w", sysaAddr, err)
		}
		conn, err := grpc.DialContext(ctx,
			sysaAddr,
			grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{ServerName: host})),
			grpc.WithPerRPCCredentials(&systemACredentials{p.Provider}),
		)

		if err != nil {
			cancel()
			return client, err
		}

		client, err = p.Provider.BuildClient(ctx, conn)

		if err != nil {
			cancel()
			return client, err
		}
		p.ctx, p.cancel, p.client = ctx, cancel, client
	}

	p.count++
	return p.client, nil
}

func (p *systemAPool[T]) Done(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.count == 0 {
		dlog.Warnf(ctx, "Double Done() for a systema pool for clients %s", p.Name)
		return nil
	}

	p.count--
	if p.count == 0 {
		p.cancel()
		err := p.client.Close(ctx)
		var client T
		p.ctx, p.cancel, p.client = nil, nil, client
		return err
	}
	return nil
}

type systemACredentials struct {
	headers HeaderProvider
}

// GetRequestMetadata implements credentials.PerRPCCredentials.
func (c *systemACredentials) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	headers := make(map[string]string)
	if apiKey, err := c.headers.GetAPIKey(ctx); err != nil {
		return nil, err
	} else if apiKey != "" {
		headers[ApiKeyHeader] = apiKey
	}
	if installID, err := c.headers.GetInstallID(ctx); err != nil {
		return nil, err
	} else if installID != "" {
		headers[InstallIDHeader] = installID
	} else if _, ok := headers[ApiKeyHeader]; !ok {
		return nil, fmt.Errorf("at least one of ApiKey and InstallID must return a non-empty string")
	}
	if extra, err := c.headers.GetExtraHeaders(ctx); err != nil {
		return nil, err
	} else {
		for header, val := range extra {
			if val != "" {
				headers[header] = val
			}
		}
	}
	return headers, nil
}

// RequireTransportSecurity implements credentials.PerRPCCredentials.
func (c *systemACredentials) RequireTransportSecurity() bool {
	return true
}
