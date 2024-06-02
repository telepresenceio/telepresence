package dnet

import (
	"context"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc/resolver"

	"github.com/datawire/k8sapi/pkg/k8sapi"
)

const K8sPFScheme = "k8spf"

type pfResolverBuilder struct {
	context.Context
}

type k8sResolver struct {
	ctx      context.Context
	cancel   context.CancelFunc
	endPoint string
	cc       resolver.ClientConn
	wg       sync.WaitGroup
	rn       chan struct{}
}

// ResolveNow invoke an immediate resolution of the target that this
// dnsResolver watches.
func (d *k8sResolver) ResolveNow(resolver.ResolveNowOptions) {
	select {
	case d.rn <- struct{}{}:
	default:
	}
}

func (d *k8sResolver) Close() {
	d.cancel()
	d.wg.Wait()
}

func (d *k8sResolver) watcher() {
	defer d.wg.Done()
	backoffIndex := 1
	for {
		pa, err := resolve(d.ctx, k8sapi.GetK8sInterface(d.ctx), d.endPoint)
		if err != nil {
			// Report error to the underlying grpc.ClientConn.
			d.cc.ReportError(err)
		} else {
			err = d.cc.UpdateState(resolver.State{Addresses: []resolver.Address{
				{Addr: fmt.Sprintf("%s.%s:%d", pa.name, pa.namespace, pa.port)},
			}})
		}

		if err == nil {
			// Success resolving, wait for the next ResolveNow.
			backoffIndex = 1
			select {
			case <-d.ctx.Done():
				return
			case <-d.rn:
			}
		} else {
			backoffIndex++
		}
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(time.Duration(backoffIndex*backoffIndex) * time.Second):
		}
	}
}

func (p pfResolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, opts resolver.BuildOptions) (resolver.Resolver, error) {
	if target.URL.Host != "" {
		return nil, fmt.Errorf("invalid (non-empty) authority: %v", target.URL.Host)
	}
	if target.URL.Scheme != K8sPFScheme {
		return nil, fmt.Errorf("invalid scheme: %v", target.URL.Scheme)
	}
	ctx, cancel := context.WithCancel(p.Context)
	rs := k8sResolver{
		ctx:      ctx,
		cancel:   cancel,
		cc:       cc,
		rn:       make(chan struct{}),
		endPoint: target.Endpoint(),
	}
	rs.wg.Add(1)
	go rs.watcher()
	return &rs, nil
}

func (p pfResolverBuilder) Scheme() string {
	return K8sPFScheme
}

func NewResolver(ctx context.Context) resolver.Builder {
	return pfResolverBuilder{Context: ctx}
}
