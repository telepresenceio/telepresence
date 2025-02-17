package agentpf

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/puzpuzpuz/xsync/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8sclient"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type client struct {
	// Mutex protects the following fields (the rest is immutable)
	//   info.intercepted
	//   cli
	//   cancelClient
	//   cancelDialWatch
	// cli and cancelClient are both safe to use without a mutex once the ready channel is closed.
	sync.RWMutex
	cli             agent.AgentClient
	session         *manager.SessionInfo
	info            *manager.AgentPodInfo
	ready           chan error
	remove          func()
	cancelClient    context.CancelFunc
	cancelDialWatch context.CancelFunc
	tunnelCount     int32
	infant          atomic.Bool
}

func (ac *client) String() string {
	if ac == nil {
		return "<nil>"
	}
	ai := ac.info
	return fmt.Sprintf("%s(%s), port %d, dormant %t", ai.PodName, net.IP(ai.PodIp), ai.ApiPort, ac.dormant())
}

func (ac *client) ensureConnect(ctx context.Context) (err error) {
	if ac.infant.CompareAndSwap(true, false) {
		go ac.connect(ctx, func() {
			ac.remove()
		})
	}
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-ac.ready:
		// Put status on the channel for next call to ensureConnect.
		ac.ready <- err
	}
	return err
}

func (ac *client) Tunnel(ctx context.Context, opts ...grpc.CallOption) (tunnel.Client, error) {
	if err := ac.ensureConnect(ctx); err != nil {
		return nil, err
	}
	ac.RLock()
	cli := ac.cli
	ac.RUnlock()
	if cli == nil {
		// Client was closed.
		return nil, io.EOF
	}
	dlog.Tracef(ctx, "%s(%s) creating Tunnel over gRPC", ac, net.IP(ac.info.PodIp))
	tc, err := cli.Tunnel(ctx, opts...)
	if err != nil {
		dlog.Tracef(ctx, "%s(%s) failed to create Tunnel over gRPC: %v", ac, net.IP(ac.info.PodIp), err)
		return nil, err
	}
	atomic.AddInt32(&ac.tunnelCount, 1)
	dlog.Tracef(ctx, "%s(%s) have %d active tunnels", ac, net.IP(ac.info.PodIp), ac.tunnelCount)
	go func() {
		<-ctx.Done()
		atomic.AddInt32(&ac.tunnelCount, -1)
		dlog.Tracef(ctx, "%s(%s) have %d active tunnels", ac, net.IP(ac.info.PodIp), ac.tunnelCount)
	}()
	return tc, nil
}

func (ac *client) connect(ctx context.Context, deleteMe func()) {
	dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
	defer dialCancel()

	var err error
	defer func() {
		if err != nil {
			deleteMe()
		}
		ac.ready <- err
	}()

	var conn *grpc.ClientConn
	var cli agent.AgentClient

	conn, cli, _, err = k8sclient.ConnectToAgent(dialCtx, ac.info.PodName, ac.info.Namespace, uint16(ac.info.ApiPort))
	if err != nil {
		return
	}

	ac.Lock()
	ac.cli = cli
	ac.cancelClient = func() {
		// Need to run this in a separate thread to avoid deadlock.
		go func() {
			conn.Close()
			ac.Lock()
			ac.cancelClient = nil
			ac.cli = nil
			ac.infant.Store(true)
			ac.Unlock()
		}()
	}
	intercepted := ac.info.Intercepted
	ac.Unlock()
	if intercepted {
		err = ac.startDialWatcherReady(ctx)
	}
}

func (ac *client) dormant() bool {
	if ac.infant.Load() || atomic.LoadInt32(&ac.tunnelCount) > 0 {
		return false
	}
	ac.RLock()
	dormant := ac.cli != nil && !ac.info.Intercepted
	ac.RUnlock()
	return dormant
}

func (ac *client) intercepted() bool {
	ac.RLock()
	ret := ac.info.Intercepted
	ac.RUnlock()
	return ret
}

func (ac *client) cancel() bool {
	ac.RLock()
	cc := ac.cancelClient
	cdw := ac.cancelDialWatch
	ac.RUnlock()
	didCancel := false
	if cc != nil {
		didCancel = true
		cc()
	}
	if cdw != nil {
		didCancel = true
		cdw()
	}
	return didCancel
}

func (ac *client) setIntercepted(ctx context.Context, k string, status bool) {
	ac.Lock()
	oldStatus := ac.info.Intercepted
	ac.info.Intercepted = status
	cdw := ac.cancelDialWatch
	ac.Unlock()
	if status == oldStatus {
		return
	}
	if status {
		dlog.Debugf(ctx, "Agent %s changed to intercepted", k)
		go func() {
			if err := ac.startDialWatcher(ctx); err != nil {
				dlog.Errorf(ctx, "failed to start client watcher for %s: %v", k, err)
			}
		}()
		// This agent is now intercepting. Start a dial watcher.
	} else {
		// This agent is no longer intercepting. Stop the dial watcher
		dlog.Debugf(ctx, "Agent %s changed to not intercepted", k)
		if cdw != nil {
			cdw()
		}
	}
}

func (ac *client) startDialWatcher(ctx context.Context) error {
	// Not called from the startup go routine, so wait for that routine to finish
	if err := ac.ensureConnect(ctx); err != nil {
		return err
	}
	return ac.startDialWatcherReady(ctx)
}

func (ac *client) startDialWatcherReady(ctx context.Context) error {
	ac.RLock()
	cli := ac.cli
	running := ac.cancelDialWatch != nil
	ac.RUnlock()
	if running {
		return nil
	}
	if cli == nil {
		return fmt.Errorf("agent connection closed")
	}
	ctx, cancel := context.WithCancel(ctx)

	// Create the dial watcher
	dlog.Debugf(ctx, "watching dials from agent pod %s", ac)
	watcher, err := ac.cli.WatchDial(ctx, ac.session)
	if err != nil {
		cancel()
		return err
	}

	ac.Lock()
	ac.cancelDialWatch = func() {
		ac.Lock()
		ac.info.Intercepted = false
		ac.cancelDialWatch = nil
		ac.Unlock()
		cancel()
	}
	ac.Unlock()

	go func() {
		err := tunnel.DialWaitLoop(ctx, tunnel.ClientToAgent, tunnel.AgentProvider(ac.cli), watcher, tunnel.SessionID(ac.session.SessionId))
		if err != nil {
			dlog.Error(ctx, err)
		}
		// The traffic-agent closed the dial watcher which means that it's terminating.
		ac.cancel()
	}()
	return nil
}

type Clients interface {
	GetClient(netip.Addr) tunnel.Provider
	WatchAgentPods(ctx context.Context, rmc manager.ManagerClient) error
	WaitForIP(ctx context.Context, timeout time.Duration, ip netip.Addr) error
	WaitForWorkload(ctx context.Context, timeout time.Duration, name string) error
	GetWorkloadClient(workload string) (ag tunnel.Provider)
	SetProxyVia(workload string)
}

type clients struct {
	session   *manager.SessionInfo
	clients   *xsync.MapOf[string, *client]
	ipWaiters *xsync.MapOf[netip.Addr, chan struct{}]
	wlWaiters *xsync.MapOf[string, chan struct{}]
	proxyVias *xsync.MapOf[string, struct{}]
	disabled  atomic.Bool
}

func NewClients(session *manager.SessionInfo) Clients {
	return &clients{
		session:   session,
		clients:   xsync.NewMapOf[string, *client](),
		ipWaiters: xsync.NewMapOf[netip.Addr, chan struct{}](),
		wlWaiters: xsync.NewMapOf[string, chan struct{}](),
		proxyVias: xsync.NewMapOf[string, struct{}](),
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
func (s *clients) GetClient(ip netip.Addr) (pvd tunnel.Provider) {
	if s.disabled.Load() {
		return nil
	}
	var primary, secondary, ternary tunnel.Provider
	s.clients.Range(func(_ string, c *client) bool {
		podIP, ok := netip.AddrFromSlice(c.info.PodIp)
		switch {
		case ok && ip == podIP:
			primary = c
		case c.intercepted():
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

func (s *clients) SetProxyVia(workload string) {
	s.proxyVias.Store(workload, struct{}{})
}

func (s *clients) isProxyVIA(info *manager.AgentPodInfo) bool {
	_, isPV := s.proxyVias.Load(info.WorkloadName)
	return isPV
}

func (s *clients) hasWaiterFor(info *manager.AgentPodInfo) bool {
	if podIP, ok := netip.AddrFromSlice(info.PodIp); ok {
		if _, isW := s.ipWaiters.Load(podIP); isW {
			return true
		}
	}
	if _, isW := s.wlWaiters.Load(info.WorkloadName); isW {
		return true
	}
	return false
}

func (s *clients) WatchAgentPods(ctx context.Context, rmc manager.ManagerClient) error {
	dlog.Debug(ctx, "WatchAgentPods starting")
	defer func() {
		activeCount := 0
		s.clients.Range(func(_ string, ac *client) bool {
			if ac.cancel() {
				activeCount++
			}
			return true
		})
		dlog.Debugf(ctx, "WatchAgentPods ending with %d clients still active", activeCount)
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
				// User daemon departed from the session.
				return nil
			}
			switch status.Code(err) {
			case codes.OK:
				err = s.updateClients(ctx, ais.Agents)
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
		if podIP, ok := netip.AddrFromSlice(ac.info.PodIp); ok {
			if waiter, ok := s.ipWaiters.LoadAndDelete(podIP); ok {
				close(waiter)
			}
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

func (s *clients) WaitForIP(ctx context.Context, timeout time.Duration, ip netip.Addr) error {
	if s.disabled.Load() {
		return nil
	}
	waitOn, ok := s.ipWaiters.Compute(ip, func(oldValue chan struct{}, loaded bool) (chan struct{}, bool) {
		if loaded {
			return oldValue, false
		}
		found := false
		s.clients.Range(func(k string, ac *client) bool {
			if podIP, ok := netip.AddrFromSlice(ac.info.PodIp); ok && ip == podIP {
				found = true
				return false
			}
			return true
		})
		if found {
			return nil, true
		}
		return make(chan struct{}), false
	})
	if ok {
		if err := s.waitWithTimeout(ctx, timeout, waitOn); err != nil {
			return err
		}
	}
	// Ensure that the client we're waiting for is ready.
	var cl *client
	s.clients.Range(func(k string, ac *client) bool {
		if acIP, ok := netip.AddrFromSlice(ac.info.PodIp); ok && ip == acIP {
			cl = ac
			return false
		}
		return true
	})
	if cl == nil {
		return status.Error(codes.NotFound, "no client available")
	}
	return cl.ensureConnect(ctx)
}

func (s *clients) WaitForWorkload(ctx context.Context, timeout time.Duration, name string) error {
	if s.disabled.Load() {
		return nil
	}

	// Create a channel to subscribe to, but only if the agent doesn't already exist.
	waitOn, ok := s.wlWaiters.Compute(name, func(oldValue chan struct{}, loaded bool) (chan struct{}, bool) {
		if loaded {
			return oldValue, false
		}
		found := false
		s.clients.Range(func(k string, ac *client) bool {
			if ac.info.WorkloadName == name {
				found = true
				return false
			}
			return true
		})
		if found {
			return nil, true
		}
		return make(chan struct{}), false
	})
	if ok {
		return s.waitWithTimeout(ctx, timeout, waitOn)
	}
	// No chan created because the agent already exists
	return nil
}

func (s *clients) updateClients(ctx context.Context, ais []*manager.AgentPodInfo) error {
	defer s.notifyWaiters()

	if dlog.MaxLogLevel(ctx) >= dlog.LogLevelDebug {
		ns := make([]string, len(ais))
		for i, ac := range ais {
			ns[i] = fmt.Sprintf("%s(%s)", ac.PodName, net.IP(ac.PodIp))
		}
		dlog.Debugf(ctx, "updateClients %s", ns)
	}
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
			dlog.Debugf(ctx, "disabling, because traffic-agent doesn't report pod name")
			s.disabled.Store(true)
			return nil
		}
	}

	deleteClient := func(k string) {
		s.clients.Compute(k, func(oldValue *client, loaded bool) (*client, bool) {
			if loaded {
				dlog.Debugf(ctx, "Deleting agent %s", k)
				oldValue.cancel()
			}
			return nil, true
		})
	}

	// Cancel clients that no longer exist.
	s.clients.Range(func(k string, _ *client) bool {
		if _, ok := aim[k]; !ok {
			deleteClient(k)
		}
		return true
	})

	// Refresh current clients
	for k, ai := range aim {
		if ac, ok := s.clients.Load(k); ok {
			ac.setIntercepted(ctx, k, ai.Intercepted)
		}
	}

	addClient := func(k string, ai *manager.AgentPodInfo) {
		_, _ = s.clients.Compute(k, func(oldValue *client, loaded bool) (*client, bool) {
			if loaded {
				return oldValue, false
			}
			ac := &client{
				ready:   make(chan error, 1),
				session: s.session,
				remove: func() {
					deleteClient(k)
				},
				info: ai,
			}
			ac.infant.Store(true)
			dlog.Debugf(ctx, "Adding agent pod %s (%s)", k, net.IP(ai.PodIp))
			return ac, false
		})
	}

	// Add clients for newly arrived agents.
	for k, ai := range aim {
		addClient(k, ai)
	}

	// Terminate all dormant agents except the last one.
	dormantCount := 0
	s.clients.Range(func(k string, ac *client) bool {
		if ac.dormant() && !s.isProxyVIA(ac.info) && !s.hasWaiterFor(ac.info) {
			dormantCount++
			if dormantCount > 1 {
				ac.cancel()
			}
		}
		return true
	})
	if dormantCount > 1 {
		dlog.Debugf(ctx, "Cancelled %d dormant clients", dormantCount-1)
	}
	return nil
}
