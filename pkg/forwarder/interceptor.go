package forwarder

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"sync"

	core "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type Interceptor interface {
	io.Closer
	Tag() tunnel.Tag
	InterceptId() string
	InterceptInfo() *restapi.InterceptInfo
	Serve(context.Context, chan<- netip.AddrPort) error
	SetIntercepting(*manager.InterceptInfo)
	SetStreamProvider(tunnel.ClientStreamProvider)
	Target() (string, uint16)
	AddWiretap(*manager.InterceptInfo)
	WiretapIDs() []string
	HasWiretap(id string) bool
	RemoveWiretap(id string)
}

type interceptor struct {
	mu sync.Mutex

	lCtx       context.Context
	lCancel    context.CancelFunc
	listenPort uint16

	tCtx           context.Context
	tCancel        context.CancelFunc
	tag            tunnel.Tag
	targetHost     string
	targetPort     uint16
	streamProvider tunnel.ClientStreamProvider
	wiretaps       map[string]*manager.InterceptInfo

	intercept *manager.InterceptInfo
}

func NewInterceptor(from types.PortAndProto, tag tunnel.Tag, targetHost string, targetPort uint16) Interceptor {
	switch from.Proto {
	case core.ProtocolTCP:
		return newTCP(from.Port, tag, targetHost, targetPort)
	case core.ProtocolUDP:
		return newUDP(from.Port, tag, targetHost, targetPort)
	default:
		panic(fmt.Errorf("unsupported protocol %s", from.Proto))
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

func (f *interceptor) Target() (string, uint16) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.targetHost, f.targetPort
}

func (f *interceptor) InterceptInfo() *restapi.InterceptInfo {
	ii := &restapi.InterceptInfo{}
	f.mu.Lock()
	if f.intercept != nil {
		ii.Intercepted = true
		ii.Metadata = f.intercept.Metadata
	}
	f.mu.Unlock()
	return ii
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

func (f *interceptor) SetIntercepting(intercept *manager.InterceptInfo) {
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
		dlog.Debugf(f.lCtx, "Forward target changed from intercept %s to %s",
			iceptInfo(f.intercept), iputil.JoinHostPort(f.targetHost, f.targetPort))
	} else {
		if f.intercept == nil {
			dlog.Debugf(f.lCtx, "Forward target changed from %s to intercept %s",
				iputil.JoinHostPort(f.targetHost, f.targetPort), iceptInfo(intercept))
		} else {
			if f.intercept.Id == intercept.Id {
				return
			}
			dlog.Debugf(f.lCtx, "Forward target changed from intercept %s to intercept %q", iceptInfo(f.intercept), iceptInfo(intercept))
		}
	}

	// Drop existing connections
	f.tCancel()

	// Set up new target and lifetime
	f.tCtx, f.tCancel = context.WithCancel(f.lCtx)
	f.intercept = intercept
}

func (f *interceptor) Tag() tunnel.Tag {
	return f.tag
}
