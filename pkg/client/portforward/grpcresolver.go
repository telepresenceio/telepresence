package portforward

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/serviceconfig"
)

const (
	K8sPFScheme = "k8spf"
)

type resolverBuilder struct {
	context.Context
	knownPod *PodAddress
}

func NewResolver(ctx context.Context, knownPod *PodAddress) resolver.Builder {
	return resolverBuilder{Context: ctx, knownPod: knownPod}
}

func (p resolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (rs resolver.Resolver, err error) {
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
			lastPA:   p.knownPod,
		}
		rs.wg.Add(1)
		go rs.watcher()
		return rs, nil
	}

	var state resolver.State
	pa, err := resolve(p.Context, target.Endpoint())
	if err == nil {
		state = pa.state()
	} else {
		state = resolver.State{ServiceConfig: &serviceconfig.ParseResult{Err: err}}
	}
	return &noopResolver{}, cc.UpdateState(state)
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
	lastPA   *PodAddress
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
	if d.lastPA != nil {
		err := d.cc.UpdateState(d.lastPA.state())
		if err == nil {
			// Wait for next ResolveNow
			select {
			case <-d.ctx.Done():
				return
			case <-d.rn:
			}
		}
	}
	ebo := backoff.NewExponentialBackOff(
		backoff.WithInitialInterval(2*time.Second),
		backoff.WithMaxInterval(7*time.Second),
		backoff.WithMaxElapsedTime(120*time.Second),
	)
	for {
		pa, err := resolve(d.ctx, d.endPoint)
		if err != nil {
			// Report error to the underlying grpc.ClientConn.
			d.cc.ReportError(err)
		} else if d.lastPA == nil || *pa != *d.lastPA {
			err = d.cc.UpdateState(pa.state())
		}

		if err == nil {
			// Success resolving, wait for the next ResolveNow.
			d.lastPA = pa
			select {
			case <-d.ctx.Done():
				return
			case <-d.rn:
				ebo.Reset()
				continue
			}
		}
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(ebo.NextBackOff()):
		}
	}
}
