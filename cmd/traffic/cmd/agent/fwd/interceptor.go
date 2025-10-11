package fwd

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sync"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type Interceptor interface {
	forwarder.Forwarder

	InterceptId() string
	SetIntercepting(context.Context, *manager.InterceptInfo)
	SetInterceptingMultiple(context.Context, []*manager.InterceptInfo)
	SetStreamProvider(tunnel.ClientStreamProvider)
	AddWiretap(*manager.InterceptInfo)
	WiretapIDs() []string
	HasWiretap(id string) bool
	RemoveWiretap(id string)
	PruneTo(ctx context.Context, ids []string)

	// DispatchByMechanism gives the interceptor a chance to handle a connection
	// using any mechanism-specific behavior (e.g., HTTP-aware handling). It
	// returns true if the connection was fully handled and no further processing
	// should occur.
	DispatchByMechanism(ctx context.Context, conn net.Conn, intercept *manager.InterceptInfo) (bool, error)

	// InterceptInfos returns the intercepts that are currently being handled by this interceptor. The
	// wiretaps are not included.
	InterceptInfos() []*manager.InterceptInfo
}

type interceptor struct {
	forwarder.Forwarder
	mu         sync.Mutex
	lCtx       context.Context
	lCancel    context.CancelFunc
	listenPort uint16

	tCtx           context.Context
	tCancel        context.CancelFunc
	tag            tunnel.Tag
	target         netip.AddrPort
	streamProvider tunnel.ClientStreamProvider
	wiretaps       map[string]*manager.InterceptInfo

	intercept *manager.InterceptInfo
}

func NewInterceptor(ctx context.Context, from types.PortAndProto, tag tunnel.Tag, target netip.AddrPort) Interceptor {
	switch from.Proto {
	case types.ProtoTCP:
		return newTCP(ctx, from, tag, target)
	case types.ProtoUDP:
		return newUDP(ctx, from, tag, target)
	default:
		panic(fmt.Errorf("unsupported protocol %s", from.Proto))
	}
}

func newInterceptor(ctx context.Context, listenPort types.PortAndProto, tag tunnel.Tag, target netip.AddrPort) *interceptor {
	ctx, cancel := context.WithCancel(ctx)
	fx := &interceptor{
		Forwarder: forwarder.New(listenPort, tag, target),
		lCtx:      ctx,
		lCancel:   cancel,
	}
	return fx
}

func (f *interceptor) InterceptInfos() (infos []*manager.InterceptInfo) {
	f.mu.Lock()
	if f.intercept != nil {
		infos = []*manager.InterceptInfo{f.intercept}
	}
	f.mu.Unlock()
	return infos
}

func (f *interceptor) PruneTo(ctx context.Context, ids []string) {
	// Drop wiretaps that are no longer wanted
	for _, wid := range f.WiretapIDs() {
		if !slices.Contains(ids, wid) {
			f.RemoveWiretap(wid)
		}
	}
	// Remove the intercept if it's no longer wanted
	iid := f.InterceptId()
	if iid != "" && !slices.Contains(ids, iid) {
		f.SetIntercepting(ctx, nil)
	}
}

func (f *interceptor) SetStreamProvider(streamProvider tunnel.ClientStreamProvider) {
	f.mu.Lock()
	f.streamProvider = streamProvider
	f.mu.Unlock()
}

func (f *interceptor) Close() error {
	f.lCancel()
	return nil
}

func (f *interceptor) InterceptId() (id string) {
	f.mu.Lock()
	if f.intercept != nil {
		id = f.intercept.Id
	}
	f.mu.Unlock()
	return id
}

func (f *interceptor) AddWiretap(intercept *manager.InterceptInfo) {
	f.mu.Lock()
	if f.wiretaps == nil {
		f.wiretaps = make(map[string]*manager.InterceptInfo)
	}
	f.wiretaps[intercept.Id] = intercept
	f.mu.Unlock()
}

func (f *interceptor) HasWiretap(id string) bool {
	f.mu.Lock()
	_, ok := f.wiretaps[id]
	f.mu.Unlock()
	return ok
}

func (f *interceptor) WiretapIDs() []string {
	f.mu.Lock()
	ids := make([]string, 0, len(f.wiretaps))
	for id := range f.wiretaps {
		ids = append(ids, id)
	}
	f.mu.Unlock()
	return ids
}

func (f *interceptor) RemoveWiretap(id string) {
	f.mu.Lock()
	delete(f.wiretaps, id)
	f.mu.Unlock()
}

func (f *interceptor) SetIntercepting(ctx context.Context, intercept *manager.InterceptInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()

	iceptInfo := func(ii *manager.InterceptInfo) string {
		is := ii.Spec
		return fmt.Sprintf("'%s' (%s)", is.Name, iputil.JoinHostPort(is.Client, uint16(is.TargetPort)))
	}
	if intercept == nil {
		if f.intercept == nil {
			return
		}
		dlog.Debugf(ctx, "Forward target changed from intercept %s to %s",
			iceptInfo(f.intercept), f.target)
	} else {
		if f.intercept == nil {
			dlog.Debugf(ctx, "Forward target changed from %s to intercept %s",
				f.target, iceptInfo(intercept))
		} else {
			if f.intercept.Id == intercept.Id {
				return
			}
			dlog.Debugf(ctx, "Forward target changed from intercept %s to intercept %q", iceptInfo(f.intercept), iceptInfo(intercept))
		}
	}
	f.intercept = intercept
	if f.lCtx != nil {
		// Drop existing connections
		f.tCancel()

		// Set up a new target and lifetime
		f.tCtx, f.tCancel = context.WithCancel(f.lCtx)
	}
}

func (f *interceptor) SetInterceptingMultiple(ctx context.Context, intercepts []*manager.InterceptInfo) {
	// For TCP interceptors, multiple intercepts with different routing isn't supported.
	// Use the first non-wiretap intercept, or nil if none exist.
	var activeIntercept *manager.InterceptInfo
	for _, intercept := range intercepts {
		if !intercept.Spec.Wiretap {
			activeIntercept = intercept
			break
		}
	}
	f.SetIntercepting(ctx, activeIntercept)
}

func (f *interceptor) Tag() tunnel.Tag {
	return f.tag
}
