package fwd

import (
	"context"
	"fmt"
	"net/netip"
	"sync"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type Interceptor interface {
	forwarder.Forwarder

	// IsHTTP returns true if this interceptor is for HTTP traffic.
	IsHTTP() bool

	SetIntercepting([]*manager.InterceptInfo)
	SetWiretapping([]*manager.InterceptInfo)
	SetStreamProvider(tunnel.ClientStreamProvider)
	Tag() tunnel.Tag

	// InterceptSpecs returns the intercepts that are currently being handled by this interceptor. The
	// wiretaps are not included.
	InterceptSpecs(callerID string) []*manager.InterceptSpec
}

type interceptor struct {
	forwarder.Forwarder
	mu             sync.Mutex
	lCtx           context.Context
	streamProvider tunnel.ClientStreamProvider
	wiretaps       interceptControllerMap
	intercepts     interceptControllerMap
}

func NewInterceptor(ctx context.Context, from types.PortAndProto, tag tunnel.Tag, target netip.AddrPort) Interceptor {
	switch from.Proto {
	case types.ProtoTCP:
		return NewTCPInterceptor(ctx, from, tag, nil, target)
	case types.ProtoUDP:
		return newUDP(ctx, from, tag, target)
	default:
		panic(fmt.Errorf("unsupported protocol %s", from.Proto))
	}
}

func newInterceptor(ctx context.Context, listenPort types.PortAndProto, tag tunnel.Tag, target netip.AddrPort) *interceptor {
	fx := &interceptor{
		Forwarder:  forwarder.New(listenPort, tag, target),
		lCtx:       ctx,
		intercepts: make(interceptControllerMap),
		wiretaps:   make(interceptControllerMap),
	}
	return fx
}

func (f *interceptor) InterceptSpecs(callerID string) []*manager.InterceptSpec {
	f.mu.Lock()
	sis := f.intercepts.sorted()
	f.mu.Unlock()
	infos := make([]*manager.InterceptSpec, 0, len(sis))
	for _, si := range sis {
		if callerID != "" && callerID != si.id {
			continue
		}
		infos = append(infos, si.InterceptSpec)
	}
	return infos
}

func (f *interceptor) SetStreamProvider(streamProvider tunnel.ClientStreamProvider) {
	f.mu.Lock()
	f.streamProvider = streamProvider
	f.mu.Unlock()
}

func (f *interceptor) SetIntercepting(infos []*manager.InterceptInfo) {
	f.mu.Lock()
	f.intercepts.reconcile(f.lCtx, infos)
	clog.Debugf(f.lCtx, "SetIntercepting %d intercepts", len(f.intercepts))
	f.mu.Unlock()
}

func (f *interceptor) SetWiretapping(infos []*manager.InterceptInfo) {
	f.mu.Lock()
	f.wiretaps.reconcile(f.lCtx, infos)
	clog.Debugf(f.lCtx, "SetWiretapping %d wiretaps", len(f.wiretaps))
	f.mu.Unlock()
}
