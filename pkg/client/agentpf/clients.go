package agentpf

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/puzpuzpuz/xsync/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/types"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	tpClient "github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/watcher"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type client struct {
	// Mutex protects the following fields (the rest is immutable)
	//   info.intercepted
	//   cli
	//   cancelClient
	//   cancelDialWatch
	// cli and cancelClient are both safe to use without a mutex once the ready channel is closed.
	*k8s.Cluster
	sync.RWMutex
	cli             agent.AgentClient
	session         *manager.SessionInfo
	info            *manager.AgentPodInfo
	remove          func()
	cancelClient    context.CancelFunc
	cancelDialWatch context.CancelFunc
	dialWatchID     uint64
	tunnelCount     int32
	lastActive      int64
}

const (
	dormantLingerTime             = 5 * time.Second
	dialWatcherReconnectInitial   = 250 * time.Millisecond
	dialWatcherReconnectMax       = 5 * time.Second
	dialWatcherReconnectResetTime = 30 * time.Second
)

func (ac *client) String() string {
	if ac == nil {
		return "<nil>"
	}
	ai := ac.info
	return fmt.Sprintf("%s(%s), port %d", ai.PodName, net.IP(ai.PodIp), ai.ApiPort)
}

func (ac *client) Tunnel(ctx context.Context, opts ...grpc.CallOption) (tunnel.Client, error) {
	cli, err := ac.ensureConnect(ctx)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	clog.Tracef(ctx, "%s(%s) creating Tunnel over gRPC", ac, net.IP(ac.info.PodIp))
	tc, err := cli.Tunnel(ctx, opts...)
	elapsed := time.Since(start)
	if err != nil {
		clog.Warnf(ctx, "%s(%s) failed to create Tunnel over gRPC after %s: %v", ac, net.IP(ac.info.PodIp), elapsed.Round(time.Millisecond), err)
		return nil, err
	}
	if elapsed > time.Second {
		clog.Warnf(ctx, "%s(%s) created Tunnel over gRPC slowly in %s", ac, net.IP(ac.info.PodIp), elapsed.Round(time.Millisecond))
	}
	atomic.AddInt32(&ac.tunnelCount, 1)
	clog.Tracef(ctx, "%s(%s) have %d active tunnels", ac, net.IP(ac.info.PodIp), atomic.LoadInt32(&ac.tunnelCount))
	go func() {
		<-ctx.Done()
		tc := atomic.LoadInt32(&ac.tunnelCount)
		if tc > 0 && atomic.CompareAndSwapInt32(&ac.tunnelCount, tc, tc-1) {
			clog.Tracef(ctx, "%s(%s) have %d active tunnels", ac, net.IP(ac.info.PodIp), tc-1)
		}
	}()
	atomic.StoreInt64(&ac.lastActive, time.Now().UnixNano())
	return tc, nil
}

func (ac *client) ensureConnect(ctx context.Context) (agent.AgentClient, error) {
	ac.Lock()
	defer ac.Unlock()
	return ac.ensureConnectLocked(ctx)
}

func (ac *client) ensureConnectLocked(ctx context.Context) (agent.AgentClient, error) {
	if ac.info.Intercepted {
		ac.startDialWatcherLocked()
	}

	if ac.cli == nil {
		tos := tpClient.GetConfig(ac).Timeouts()
		dialCtx, dialCancel := tos.TimeoutContext(ctx, tpClient.TimeoutTrafficAgentConnect)
		defer dialCancel()

		ai := ac.info
		conn, cli, _, err := ac.ConnectToAgent(dialCtx, ai.Namespace, ai.PodName, uint16(ai.ApiPort), types.UID(ai.PodId))
		if err != nil {
			if ac.info.Intercepted {
				return nil, err
			}

			// There's a risk for deadlock here, because of the Range iteration of the map that performs cancel. This cancel will block
			// because we're holding the lock now, and since the Range iteration holds a lock for the entry that we're about to delete,
			// that delete will block. So we let a potential cancel call continue by unlocking before we delete.
			ac.Unlock()
			ac.remove()
			ac.Lock() // Must of course lock again to prevent panic by the pending unlock.
			return nil, err
		}

		ac.cli = cli
		ac.cancelClient = func() {
			// Need to run this in a separate thread to avoid deadlock.
			go func() {
				conn.Close()
				ac.Lock()
				atomic.StoreInt32(&ac.tunnelCount, 0)
				ac.cancelClient = nil
				ac.cli = nil
				ac.Unlock()
			}()
		}
	}

	atomic.StoreInt64(&ac.lastActive, time.Now().UnixNano())
	return ac.cli, nil
}

func (ac *client) idleTime() time.Duration {
	return time.Duration(time.Now().UnixNano() - atomic.LoadInt64(&ac.lastActive))
}

func (ac *client) dormant() bool {
	if atomic.LoadInt32(&ac.tunnelCount) > 0 || ac.idleTime() < dormantLingerTime {
		return false
	}
	ac.RLock()
	dormant := ac.cli != nil && !ac.info.Intercepted
	ac.RUnlock()
	return dormant
}

func (ac *client) connected() bool {
	ac.RLock()
	ok := ac.cli != nil
	ac.RUnlock()
	return ok
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
	if cdw != nil {
		didCancel = true
		cdw()
	}
	if cc != nil {
		didCancel = true
		cc()
	}
	return didCancel
}

func (ac *client) refresh(ai *manager.AgentPodInfo) {
	var cdw context.CancelFunc
	defer func() {
		if cdw != nil {
			cdw()
		}
	}()

	ac.Lock()
	defer ac.Unlock()

	oldStatus := ac.info.Intercepted
	ac.info = ai
	if ai.Intercepted == oldStatus {
		if ai.Intercepted && ac.cancelDialWatch == nil {
			clog.Debugf(ac, "Agent %s(%s) is intercepted but has no dial watcher; ensuring connection", ai.PodName, net.IP(ai.PodIp))
			if _, err := ac.ensureConnectLocked(ac); err != nil {
				clog.Errorf(ac, "failed to ensure client watcher for %s(%s): %v", ai.PodName, net.IP(ai.PodIp), err)
			}
		}
		return
	}
	if ai.Intercepted {
		clog.Debugf(ac, "Agent %s(%s) changed to intercepted", ai.PodName, net.IP(ai.PodIp))
		if _, err := ac.ensureConnectLocked(ac); err != nil {
			clog.Errorf(ac, "failed to start client watcher for %s(%s): %v", ai.PodName, net.IP(ai.PodIp), err)
		}
	} else {
		// This agent is no longer intercepting. Stop the dial watcher
		clog.Debugf(ac, "Agent %s(%s) changed to not intercepted", ai.PodName, net.IP(ai.PodIp))
		cdw = ac.cancelDialWatch
	}
}

func (ac *client) startDialWatcherLocked() {
	if ac.cancelDialWatch != nil {
		// Already started
		return
	}
	ctx, cancel := context.WithCancel(ac)
	ac.dialWatchID++
	watchID := ac.dialWatchID
	ac.cancelDialWatch = func() {
		ac.Lock()
		if ac.dialWatchID == watchID {
			ac.cancelDialWatch = nil
		}
		ac.Unlock()
		cancel()
	}

	go ac.runDialWatcher(ctx, watchID)
}

func (ac *client) resetAgentClient() {
	ac.Lock()
	cancelClient := ac.cancelClient
	ac.cancelClient = nil
	ac.cli = nil
	atomic.StoreInt32(&ac.tunnelCount, 0)
	ac.Unlock()
	if cancelClient != nil {
		cancelClient()
	}
}

func (ac *client) runDialWatcher(ctx context.Context, watchID uint64) {
	started := time.Now()
	clog.Debugf(ctx, "dial watcher %d starting for %s", watchID, ac)
	defer func() {
		ac.Lock()
		if ac.dialWatchID == watchID {
			ac.cancelDialWatch = nil
		}
		ac.Unlock()
		clog.Debugf(
			ctx,
			"dial watcher %d stopped for %s after %s: context=%v cause=%v activeTunnels=%d intercepted=%t",
			watchID,
			ac,
			time.Since(started).Round(time.Millisecond),
			ctx.Err(),
			context.Cause(ctx),
			atomic.LoadInt32(&ac.tunnelCount),
			ac.intercepted(),
		)
	}()

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = dialWatcherReconnectInitial
	bo.MaxInterval = dialWatcherReconnectMax
	bo.MaxElapsedTime = 0
	bo.Reset()
	lastConnected := time.Now()

	for ctx.Err() == nil {
		cli, err := ac.ensureConnect(ctx)
		if err == nil {
			ac.RLock()
			session := ac.session
			ai := ac.info
			ac.RUnlock()

			clog.Debugf(ctx, "watching dials from agent pod %s(%s)", ai.PodName, net.IP(ai.PodIp))
			watchStarted := time.Now()
			dialStream, watchErr := cli.WatchDial(ctx, session)
			if watchErr == nil {
				if time.Since(lastConnected) > dialWatcherReconnectResetTime {
					bo.Reset()
				}
				lastConnected = time.Now()
				watchErr = tunnel.DialWaitLoop(ctx, tunnel.AgentToClient, tunnel.AgentProvider(cli), dialStream, tunnel.SessionID(session.SessionId))
			}
			if watchErr != nil {
				clog.Warnf(
					ctx,
					"dial watcher %d stream for %s ended after %s: %v activeTunnels=%d",
					watchID,
					ac,
					time.Since(watchStarted).Round(time.Millisecond),
					watchErr,
					atomic.LoadInt32(&ac.tunnelCount),
				)
			}
			err = watchErr
		}
		if ctx.Err() != nil {
			return
		}

		if err != nil {
			clog.Warnf(ctx, "dial watcher for %s ended; reconnecting: %v", ac, err)
			ac.resetAgentClient()
		} else {
			clog.Warnf(ctx, "dial watcher for %s ended unexpectedly; reconnecting", ac)
		}

		delay := bo.NextBackOff()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

type Clients interface {
	GetRandomAgent(context.Context) agent.AgentClient
	GetClient(netip.Addr) tunnel.Provider
	WatchAgentPods(rmc manager.ManagerClient) error
	WaitForIP(ctx context.Context, timeout time.Duration, namespace string, ip netip.Addr) error
	WaitForWorkload(timeout time.Duration, name string) error
	GetWorkloadClient(workload string) (ag tunnel.Provider)
	SetProxyVia(workload string)
}

type ipWaitKey struct {
	namespace string
	ip        netip.Addr
}

type clients struct {
	*k8s.Cluster
	session      *manager.SessionInfo
	clients      *xsync.Map[string, *client]
	ipWaiters    *xsync.Map[ipWaitKey, chan struct{}]
	wlWaiters    *xsync.Map[string, chan struct{}]
	proxyVias    *xsync.Map[string, struct{}]
	namespacesMu sync.RWMutex
	namespaces   map[string]struct{}
	disabled     atomic.Bool
}

func NewClients(cl *k8s.Cluster, session *manager.SessionInfo, namespaces []string) Clients {
	if len(namespaces) == 0 {
		namespaces = []string{cl.Namespace}
	}
	cs := &clients{
		Cluster:   cl,
		session:   session,
		clients:   xsync.NewMap[string, *client](),
		ipWaiters: xsync.NewMap[ipWaitKey, chan struct{}](),
		wlWaiters: xsync.NewMap[string, chan struct{}](),
		proxyVias: xsync.NewMap[string, struct{}](),
	}
	cs.setNamespaces(namespaces)
	return cs
}

func (s *clients) setNamespaces(namespaces []string) {
	nsSet := make(map[string]struct{}, len(namespaces))
	for _, namespace := range namespaces {
		if namespace != "" {
			nsSet[namespace] = struct{}{}
		}
	}
	s.namespacesMu.Lock()
	s.namespaces = nsSet
	s.namespacesMu.Unlock()
}

func (s *clients) namespaceList() []string {
	s.namespacesMu.RLock()
	namespaces := make([]string, 0, len(s.namespaces))
	for namespace := range s.namespaces {
		namespaces = append(namespaces, namespace)
	}
	s.namespacesMu.RUnlock()
	return namespaces
}

func (s *clients) watchesNamespace(namespace string) bool {
	if namespace == "" {
		namespace = s.Namespace
	}
	s.namespacesMu.RLock()
	_, ok := s.namespaces[namespace]
	s.namespacesMu.RUnlock()
	return ok
}

func (s *clients) agentsRequest() *manager.AgentsRequest {
	return &manager.AgentsRequest{
		Session:    s.session,
		Namespaces: s.namespaceList(),
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

// GetRandomAgent returns an active agent.AgentClient and ensures that it is kept alive
// for at least 5 seconds.
//
// The function returns nil when there are no active agents.
func (s *clients) GetRandomAgent(ctx context.Context) (aa agent.AgentClient) {
	var connected, waiting, other *client
	s.clients.Range(func(_ string, ac *client) bool {
		if ac.connected() {
			connected = ac
			return false
		}
		if s.isProxyVIA(ac.info) || s.hasWaiterFor(ac.info) {
			waiting = ac
		} else {
			other = ac
		}
		return true
	})

	var err error
	switch {
	case connected != nil:
		connected.Lock()
		connected.lastActive = time.Now().UnixNano()
		aa = connected.cli
		connected.Unlock()
	case waiting != nil:
		aa, err = waiting.ensureConnect(ctx)
	case other != nil:
		aa, err = other.ensureConnect(ctx)
	}
	if err != nil {
		clog.Warn(s, err)
	}
	return aa
}

// GetWorkloadClient returns tunnel.Provider that opens a tunnel to a traffic-agent that
// belongs to a pod created for the given workload.
//
// Proxy-via routing remains scoped to the connected namespace. The function returns nil
// when there are no agents for the given workload in that namespace.
func (s *clients) GetWorkloadClient(workload string) (pvd tunnel.Provider) {
	s.clients.Range(func(_ string, ac *client) bool {
		if ac.info.WorkloadName == workload && ac.info.Namespace == s.Namespace {
			pvd = ac
			return false
		}
		return true
	})
	return pvd
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
		if _, isW := s.ipWaiters.Load(ipWaitKey{namespace: info.Namespace, ip: podIP}); isW {
			return true
		}
	}
	if _, isW := s.wlWaiters.Load(info.WorkloadName); isW {
		return true
	}
	return false
}

func (s *clients) WatchAgentPods(rmc manager.ManagerClient) error {
	defer func() {
		activeCount := 0
		s.clients.Range(func(_ string, ac *client) bool {
			if ac.cancel() {
				activeCount++
			}
			return true
		})
		clog.Infof(s, "WatchAgentPods ending with %d clients still active", activeCount)
		s.disabled.Store(true)
	}()

	snapMap := make(map[string]*manager.AgentPodInfo)
	err := watcher.WatchWithRetry(s, "WatchAgentPodsInNamespacesDelta", tpClient.GetConfig(s).Grpc().WatchRetryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[manager.AgentPodInfoDelta], error) {
			clog.Debugf(ctx, "WatchAgentPodsInNamespacesDelta starting")
			return rmc.WatchAgentPodsInNamespacesDelta(ctx, s.agentsRequest())
		},
		func(delta *manager.AgentPodInfoDelta) error {
			clog.Debugf(s, "WatchAgentPodsInNamespacesDelta received %d upserts, %d removals", len(delta.Upserts), len(delta.Removals))
			maps.DeltaUpdate(snapMap, delta.Upserts, delta.Removals)
			return s.updateClients(maps.Values(snapMap))
		}, func() error {
			clear(snapMap)
			return nil
		})
	if err == nil || status.Code(err) != codes.Unimplemented {
		return err
	}

	// Older traffic-manager. Fall back to watching agents in the connected namespace.
	s.setNamespaces([]string{s.Namespace})
	clog.Warnf(s, "WatchAgentPodsInNamespacesDelta is not implemented by the traffic-manager, falling back to WatchAgentPodsDelta in namespace %s", s.Namespace)
	err = watcher.WatchWithRetry(s, "WatchAgentPodsDelta", tpClient.GetConfig(s).Grpc().WatchRetryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[manager.AgentPodInfoDelta], error) {
			clog.Debugf(ctx, "WatchAgentPodsDelta starting")
			return rmc.WatchAgentPodsDelta(ctx, s.session)
		},
		func(delta *manager.AgentPodInfoDelta) error {
			clog.Debugf(s, "WatchAgentPodsDelta received %d upserts, %d removals", len(delta.Upserts), len(delta.Removals))
			maps.DeltaUpdate(snapMap, delta.Upserts, delta.Removals)
			return s.updateClients(maps.Values(snapMap))
		}, func() error {
			clear(snapMap)
			return nil
		})
	if err == nil || status.Code(err) != codes.Unimplemented {
		return err
	}

	clog.Warnf(s, "WatchAgentPodsDelta is not implemented by the traffic-manager, falling back to WatchAgentPods and full snapshots")
	return watcher.WatchWithRetry(s, "WatchAgentPods", tpClient.GetConfig(s).Grpc().WatchRetryInterval,
		func(ctx context.Context) (grpc.ServerStreamingClient[manager.AgentPodInfoSnapshot], error) {
			clog.Debugf(ctx, "No delta support in traffic-manager, starting WatchAgentPods instead")
			return rmc.WatchAgentPods(ctx, s.session)
		},
		func(snapshot *manager.AgentPodInfoSnapshot) error {
			return s.updateClients(snapshot.Agents)
		}, nil)
}

func (ac *client) notify(waiter chan struct{}) {
	// a client must be connected to be able to notify
	if _, err := ac.ensureConnect(ac); err != nil {
		clog.Errorf(ac, "notifyWaiters %s (%s), ensureConnect failed: %v", ac.info.WorkloadName, net.IP(ac.info.PodIp), err)
	}
	close(waiter)
}

func (s *clients) notifyWaiters() {
	s.clients.Range(func(name string, ac *client) bool {
		if podIP, ok := netip.AddrFromSlice(ac.info.PodIp); ok {
			if waiter, ok := s.ipWaiters.LoadAndDelete(ipWaitKey{namespace: ac.info.Namespace, ip: podIP}); ok {
				ac.notify(waiter)
			}
		}
		if waiter, ok := s.wlWaiters.LoadAndDelete(ac.info.WorkloadName); ok {
			ac.notify(waiter)
		}
		return true
	})
}

func (s *clients) waitWithTimeout(timeout time.Duration, waitOn <-chan struct{}) error {
	s.notifyWaiters()
	ctx, cancel := context.WithTimeout(s, timeout)
	defer cancel()
	select {
	case <-waitOn:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *clients) WaitForIP(ctx context.Context, timeout time.Duration, namespace string, ip netip.Addr) error {
	if s.disabled.Load() {
		return status.Error(codes.Unavailable, "")
	}
	if namespace == "" {
		namespace = s.Namespace
	}
	if !s.watchesNamespace(namespace) {
		return status.Error(codes.Unavailable, "")
	}
	var cl *client
	key := ipWaitKey{namespace: namespace, ip: ip}
	waitOn, _ := s.ipWaiters.LoadOrCompute(key, func() (chan struct{}, bool) {
		s.clients.Range(func(k string, ac *client) bool {
			if podIP, ok := netip.AddrFromSlice(ac.info.PodIp); ok && ac.info.Namespace == namespace && ip == podIP {
				cl = ac
				return false
			}
			return true
		})
		if cl != nil {
			return nil, true
		}
		return make(chan struct{}), false
	})
	if cl != nil {
		_, err := cl.ensureConnect(ctx)
		return err
	}
	if err := s.waitWithTimeout(timeout, waitOn); err != nil {
		return err
	}

	// Ensure that the client we're waiting for is ready.
	s.clients.Range(func(k string, ac *client) bool {
		if acIP, ok := netip.AddrFromSlice(ac.info.PodIp); ok && ac.info.Namespace == namespace && ip == acIP {
			cl = ac
			return false
		}
		return true
	})
	if cl == nil {
		return status.Error(codes.NotFound, "no client available")
	}
	_, err := cl.ensureConnect(ctx)
	return err
}

func (s *clients) WaitForWorkload(timeout time.Duration, name string) error {
	if s.disabled.Load() {
		return nil
	}

	// Create a channel to subscribe to, but only if the agent doesn't already exist.
	waitOn, ok := s.wlWaiters.LoadOrCompute(name, func() (chan struct{}, bool) {
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
		return s.waitWithTimeout(timeout, waitOn)
	}
	// No chan created because the agent already exists
	return nil
}

func (s *clients) updateClients(ais []*manager.AgentPodInfo) error {
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
			clog.Debugf(s, "disabling, because traffic-agent doesn't report pod name")
			s.disabled.Store(true)
			return nil
		}
	}

	deleteClient := func(k string) {
		s.clients.Compute(k, func(oldValue *client, loaded bool) (*client, xsync.ComputeOp) {
			if loaded {
				clog.Debugf(s, "Deleting agent %s", k)
				oldValue.cancel()
				return nil, xsync.DeleteOp
			}
			return nil, xsync.CancelOp
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
			ac.refresh(ai)
		}
	}

	addClient := func(k string, ai *manager.AgentPodInfo) {
		ac, loaded := s.clients.LoadOrCompute(k, func() (*client, bool) {
			ac := &client{
				Cluster: s.Cluster,
				session: s.session,
				remove: func() {
					s.clients.Delete(k)
				},
				info: ai,
			}
			clog.Debugf(s, "Adding agent pod %s (%s)", k, net.IP(ai.PodIp))
			return ac, false
		})
		if !loaded && ai.Intercepted {
			clog.Debugf(s, "Newly discovered intercepted agent pod %s (%s); eagerly starting dial watcher", k, net.IP(ai.PodIp))
			if _, err := ac.ensureConnect(s); err != nil {
				clog.Errorf(s, "failed to eagerly start client watcher for %s (%s): %v", k, net.IP(ai.PodIp), err)
			}
		}
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
				clog.Debugf(s, "Deleting dormant agent %s", k)
				ac.cancel()
			}
		}
		return true
	})
	if dormantCount > 1 {
		clog.Debugf(s, "Cancelled %d dormant clients", dormantCount-1)
	}
	return nil
}
