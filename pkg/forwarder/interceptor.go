package forwarder

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type Interceptor interface {
	io.Closer
	InterceptId() string
	InterceptInfo() *restapi.InterceptInfo
	Serve(context.Context, chan<- net.Addr) error
	SetIntercepting(*manager.InterceptInfo)
	SetStreamProvider(tunnel.ClientStreamProvider)
	Target() (string, uint16)
}

type interceptor struct {
	mu sync.Mutex

	lCtx       context.Context
	lCancel    context.CancelFunc
	listenAddr net.Addr

	tCtx           context.Context
	tCancel        context.CancelFunc
	targetHost     string
	targetPort     uint16
	streamProvider tunnel.ClientStreamProvider

	intercept *manager.InterceptInfo
}

func NewInterceptor(addr net.Addr, targetHost string, targetPort uint16) Interceptor {
	switch addr := addr.(type) {
	case *net.TCPAddr:
		return newTCP(addr, targetHost, targetPort)
	case *net.UDPAddr:
		return newUDP(addr, targetHost, targetPort)
	default:
		panic(fmt.Errorf("unsupported net.Addr type %T", addr))
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
