package agentpf

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"

	"github.com/puzpuzpuz/xsync/v3"
	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8sclient"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type client struct {
	session         *manager.SessionInfo
	cancelClient    context.CancelFunc
	cancelDialWatch context.CancelFunc
	client          agent.AgentClient
	info            *manager.AgentPodInfo
	tunnelCount     int32
}

func (ac *client) String() string {
	if ac == nil {
		return "<nil>"
	}
	ai := ac.info
	return fmt.Sprintf("%s.%s:%d", ai.PodName, ai.Namespace, ai.ApiPort)
}

func (ac *client) Tunnel(ctx context.Context, opts ...grpc.CallOption) (tunnel.Client, error) {
	tc, err := ac.client.Tunnel(ctx, opts...)
	if err != nil {
		return nil, err
	}
	atomic.AddInt32(&ac.tunnelCount, 1)
	dlog.Debugf(ctx, "%s have %d active tunnels", ac, ac.tunnelCount)
	go func() {
		<-ctx.Done()
		atomic.AddInt32(&ac.tunnelCount, -1)
		dlog.Debugf(ctx, "%s have %d active tunnels", ac, ac.tunnelCount)
	}()
	return tc, nil
}

func newAgentClient(ctx context.Context, session *manager.SessionInfo, info *manager.AgentPodInfo) (*client, error) {
	pfDialer := dnet.GetPortForwardDialer(ctx)
	if pfDialer == nil {
		return nil, errors.ErrUnsupported
	}
	ctx, cancel := context.WithCancel(ctx)
	conn, cli, _, err := k8sclient.ConnectToAgent(ctx, pfDialer.Dial, info.PodName, info.Namespace, uint16(info.ApiPort))
	if err != nil {
		cancel()
		return nil, err
	}
	var ac *client
	cancelClient := func() {
		dlog.Debugf(ctx, "Cancelling port-forward to %s", ac)
		cancel()
		conn.Close()
	}
	ac = &client{
		session:      session,
		cancelClient: cancelClient,
		client:       cli,
		info:         info,
	}
	if info.Intercepted {
		if err = ac.startDialWatcher(ctx); err != nil {
			cancelClient()
			return nil, err
		}
	}
	return ac, nil
}

func (ac *client) busy() bool {
	return atomic.LoadInt32(&ac.tunnelCount) > 0
}

func (ac *client) cancel() {
	ac.cancelClient()
	if ac.info.Intercepted {
		ac.cancelDialWatch()
	}
}

func (ac *client) startDialWatcher(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)

	// Create the dial watcher
	dlog.Debugf(ctx, "watching dials from agent pod %s", ac)
	watcher, err := ac.client.WatchDial(ctx, ac.session)
	if err != nil {
		cancel()
		return err
	}
	ac.cancelDialWatch = func() {
		ac.info.Intercepted = false
		cancel()
	}
	ac.info.Intercepted = true
	go func() {
		err := tunnel.DialWaitLoop(ctx, tunnel.AgentProvider(ac.client), watcher, ac.session.SessionId)
		if err != nil {
			dlog.Error(ctx, err)
		}
	}()
	return nil
}

type Clients interface {
	GetClient(net.IP) (ag tunnel.Provider)
	WatchAgentPods(ctx context.Context, rmc manager.ManagerClient) error
	WaitForIP(ctx context.Context, timeout time.Duration, ip net.IP) error
	WaitForWorkload(ctx context.Context, timeout time.Duration, name string) error
	GetWorkloadClient(workload string) (ag tunnel.Provider)
}

type clients struct {
	session   *manager.SessionInfo
	clients   *xsync.MapOf[string, *client]
	ipWaiters *xsync.MapOf[iputil.IPKey, chan struct{}]
	wlWaiters *xsync.MapOf[string, chan struct{}]
	disabled  atomic.Bool
}

func NewClients(session *manager.SessionInfo) Clients {
	return &clients{
		session:   session,
		clients:   xsync.NewMapOf[string, *client](),
		ipWaiters: xsync.NewMapOf[iputil.IPKey, chan struct{}](),
		wlWaiters: xsync.NewMapOf[string, chan struct{}](),
	}
}

// GetClient returns tunnel.Provider that opens a tunnel to a known traffic-agent.
// The traffic-agent is chosen using the following rules in the order mentioned:
//
//  1. agent has a pod_ip that matches the given ip
//  2. agent is currently intercepted by this client
//  3. any agent
//
// The function returns nil when there are no agents in the connected namespace.
func (s *clients) GetClient(ip net.IP) (pvd tunnel.Provider) {
	var primary, secondary, ternary tunnel.Provider
	s.clients.Range(func(_ string, c *client) bool {
		switch {
		case ip.Equal(c.info.PodIp):
			primary = c
		case c.info.Intercepted:
			secondary = c
		default:
			ternary = c
		}
		return primary == nil
	})
	switch {
	case primary != nil:
		pvd = primary
	case secondary != nil:
		pvd = secondary
	default:
		pvd = ternary
	}
	return pvd
}

// GetWorkloadClient returns tunnel.Provider that opens a tunnel to a traffic-agent that
// belongs to a pod created for the given workload.
//
// The function returns nil when there are no agents for the given workload in the connected namespace.
func (s *clients) GetWorkloadClient(workload string) (pvd tunnel.Provider) {
	s.clients.Range(func(_ string, ac *client) bool {
		if ac.info.WorkloadName == workload {
			pvd = ac
			return false
		}
		return true
	})
	return
}

func (s *clients) WatchAgentPods(ctx context.Context, rmc manager.ManagerClient) error {
	dlog.Debug(ctx, "WatchAgentPods starting")
	defer func() {
		dlog.Debugf(ctx, "WatchAgentPods ending with %d clients still active", s.clients.Size())
		s.clients.Range(func(_ string, ac *client) bool {
			ac.cancel()
			return true
		})
		s.disabled.Store(true)
	}()
	backoff := 100 * time.Millisecond

outer:
	for ctx.Err() == nil {
		as, err := rmc.WatchAgentPods(ctx, s.session)
		switch status.Code(err) {
		case codes.OK:
		case codes.Unavailable:
			dtime.SleepWithContext(ctx, backoff)
			backoff *= 2
			if backoff > 15*time.Second {
				backoff = 15 * time.Second
			}
			continue outer
		case codes.Unimplemented:
			dlog.Debug(ctx, "traffic-manager does not implement WatchAgentPods")
			return nil
		default:
			err = fmt.Errorf("error when calling WatchAgents: %w", err)
			dlog.Warn(ctx, err)
			return err
		}

		for ctx.Err() == nil {
			ais, err := as.Recv()
			if errors.Is(err, io.EOF) {
				return nil
			}
			switch status.Code(err) {
			case codes.OK:
				ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "AgentClientUpdate")
				err = s.updateClients(ctx, ais.Agents)
				span.End()
				if err != nil {
					return err
				}
			case codes.Unavailable:
				dtime.SleepWithContext(ctx, backoff)
				backoff *= 2
				if backoff > 15*time.Second {
					backoff = 15 * time.Second
				}
				continue outer
			case codes.Unimplemented:
				dlog.Debug(ctx, "traffic-manager does not implement WatchAgentPods")
				return nil
			default:
				return err
			}
		}
	}
	return nil
}

func (s *clients) notifyWaiters() {
	s.clients.Range(func(name string, ac *client) bool {
		if waiter, ok := s.ipWaiters.LoadAndDelete(iputil.IPKey(ac.info.PodIp)); ok {
			close(waiter)
		}
		if waiter, ok := s.wlWaiters.LoadAndDelete(ac.info.WorkloadName); ok {
			close(waiter)
		}
		return true
	})
}

func (s *clients) waitWithTimeout(ctx context.Context, timeout time.Duration, waitOn <-chan struct{}) error {
	s.notifyWaiters()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case <-waitOn:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *clients) WaitForIP(ctx context.Context, timeout time.Duration, ip net.IP) error {
	if s.disabled.Load() {
		return nil
	}
	waitOn, _ := s.ipWaiters.LoadOrCompute(iputil.IPKey(ip), func() chan struct{} {
		return make(chan struct{})
	})
	return s.waitWithTimeout(ctx, timeout, waitOn)
}

func (s *clients) WaitForWorkload(ctx context.Context, timeout time.Duration, name string) error {
	if s.disabled.Load() {
		return nil
	}
	waitOn, _ := s.wlWaiters.LoadOrCompute(name, func() chan struct{} {
		return make(chan struct{})
	})
	return s.waitWithTimeout(ctx, timeout, waitOn)
}

func (s *clients) updateClients(ctx context.Context, ais []*manager.AgentPodInfo) error {
	defer s.notifyWaiters()

	var aim map[string]*manager.AgentPodInfo
	if len(ais) > 0 {
		aim = make(map[string]*manager.AgentPodInfo, len(ais))
		for _, ai := range ais {
			if ai.PodName != "" {
				aim[ai.PodName+"."+ai.Namespace] = ai
			}
		}
		if len(aim) == 0 {
			// The current traffic-manager injects old style clients that doesn't report a pod name.
			s.disabled.Store(true)
			return nil
		}
	}

	// Ensure that the clients still exists. Cancel the ones that don't.
	s.clients.Range(func(k string, ac *client) bool {
		if _, ok := aim[k]; !ok {
			s.clients.Delete(k)
			ac.cancel()
		}
		return true
	})

	// Refresh current clients
	for k, ai := range aim {
		ac, ok := s.clients.Load(k)
		if ok {
			if ai.Intercepted {
				if ac.info.Intercepted {
					continue
				}
				if err := ac.startDialWatcher(ctx); err != nil {
					dlog.Errorf(ctx, "failed to start client watcher for %s: %v", k, err)
				}
				// This agent is now intercepting. Start a dial watcher.
			} else {
				if !ac.info.Intercepted {
					continue
				}
				// This agent is no longer intercepting. Stop the dial watcher
				ac.cancelDialWatch()
			}
		}
	}

	// Add clients for newly arrived intercepts
	for k, ai := range aim {
		if ai.Intercepted {
			if _, ok := s.clients.Load(k); !ok {
				ac, err := newAgentClient(ctx, s.session, ai)
				if err != nil {
					dlog.Errorf(ctx, "failed to create client for %s: %v", k, err)
					continue
				}
				s.clients.Store(k, ac)
			}
		}
	}

	s.clients.Range(func(k string, ac *client) bool {
		if s.clients.Size() <= 1 {
			return false
		}
		// Terminate all non-intercepting idle agents except the last one.
		if !ac.info.Intercepted && !ac.busy() {
			s.clients.Delete(k)
			ac.cancel()
		}
		return true
	})

	// Ensure that we have at least one client (if at least one agent exists)
	if s.clients.Size() == 0 && len(aim) > 0 {
		var ai *manager.AgentPodInfo
		for _, ai = range aim {
			break
		}
		k := ai.PodName + "." + ai.Namespace
		ac, err := newAgentClient(ctx, s.session, ai)
		if err != nil {
			dlog.Errorf(ctx, "failed to create client for %s: %v", k, err)
		} else {
			s.clients.Store(k, ac)
		}
	}
	return nil
}
