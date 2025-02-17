package portforward

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"google.golang.org/grpc/resolver"
)

const (
	K8sPFScheme = "k8spf"
)

type resolverBuilder struct {
	context.Context
}

func NewResolver(ctx context.Context) resolver.Builder {
	return resolverBuilder{Context: ctx}
}

func (p resolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (resolver.Resolver, error) {
	if target.URL.Host != "" {
		return nil, fmt.Errorf("invalid (non-empty) authority: %v", target.URL.Host)
	}
	if target.URL.Scheme != K8sPFScheme {
		return nil, fmt.Errorf("invalid scheme: %v", target.URL.Scheme)
	}
	if strings.HasPrefix(target.Endpoint(), "svc/") {
		ctx, cancel := context.WithCancel(p.Context)
		rs := &svcResolver{
			ctx:      ctx,
			cancel:   cancel,
			cc:       cc,
			rn:       make(chan struct{}),
			endPoint: target.Endpoint(),
		}
		rs.wg.Add(1)
		go rs.watcher()
		return rs, nil
	}
	pa, err := resolve(p.Context, target.Endpoint())
	if err != nil {
		return nil, err
	}
	return &noopResolver{}, cc.UpdateState(pa.state())
}

func (p resolverBuilder) Scheme() string {
	return K8sPFScheme
}

type noopResolver struct{}

func (noopResolver) ResolveNow(_ resolver.ResolveNowOptions) {}

func (noopResolver) Close() {}

type svcResolver struct {
	ctx      context.Context
	cancel   context.CancelFunc
	endPoint string
	cc       resolver.ClientConn
	wg       sync.WaitGroup
	rn       chan struct{}
	lastPA   podAddress
}

// ResolveNow invoke an immediate resolution of the target that this
// dnsResolver watches.
func (d *svcResolver) ResolveNow(resolver.ResolveNowOptions) {
	select {
	case d.rn <- struct{}{}:
	default:
	}
}

func (d *svcResolver) Close() {
	d.cancel()
	d.wg.Wait()
}

func (d *svcResolver) watcher() {
	defer d.wg.Done()
	ebo := backoff.NewExponentialBackOff(
		backoff.WithInitialInterval(30*time.Second),
		backoff.WithMaxInterval(7*time.Second),
		backoff.WithMaxElapsedTime(120*time.Second),
	)
	for {
		pa, err := resolve(d.ctx, d.endPoint)
		if err != nil {
			// Report error to the underlying grpc.ClientConn.
			d.cc.ReportError(err)
		} else if *pa != d.lastPA {
			err = d.cc.UpdateState(pa.state())
		}

		if err == nil {
			// Success resolving, wait for the next ResolveNow.
			d.lastPA = *pa
			ebo.Reset()
			select {
			case <-d.ctx.Done():
				return
			case <-d.rn:
			}
		}
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(ebo.NextBackOff()):
		}
	}
}
