package quictunnel

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	auth "k8s.io/api/authorization/v1"
	core "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/watch"
	listersCore "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// Candidate is one dialable address for the QUIC endpoint, derived from the QUIC
// forwarder's Service (and, for a NodePort Service, the cluster's Nodes). It mirrors
// rpc.QuicEndpointCandidate; Discovery stays independent of the rpc package so it can
// be unit tested without constructing protobuf messages.
type Candidate struct {
	Host string
	Port int32
}

// maxCandidates caps the advertised candidate list so a large cluster's node set
// can't blow up the descriptor. Ordering is deterministic (LoadBalancer ingress
// order, or Nodes sorted by name), so truncation picks the same subset every time.
const maxCandidates = 8

const (
	// serviceWatchResync bounds how stale the Service snapshot can get if a watch
	// event is silently dropped (the same tolerance nodeagent_watch.go uses for
	// its own raw Pod watch, for the same kind of reason -- see the comment on
	// watchService below).
	serviceWatchResync = 30 * time.Second
	// serviceWatchBackoff is the pause before retrying a failed or closed watch.
	serviceWatchBackoff = 5 * time.Second
)

// Discovery watches the QUIC forwarder's Service and derives the ordered candidate
// address list that GetQuicTunnelEndpoint advertises to clients when no explicit
// externalHost override is configured. See "Zero-configuration endpoint discovery" in
// docs/plans/quic-transport/design.md.
//
// A zero Discovery is usable: Candidates returns an empty slice until Start has run
// and produced a snapshot.
type Discovery struct {
	candidates atomic.Pointer[[]Candidate]

	// mu guards svc and nodes, the inputs the most recent candidate snapshot was
	// computed from. The Service watch goroutine and the Node informer's event
	// handler both recompute and store a fresh snapshot under mu whenever either
	// input changes.
	mu    sync.Mutex
	svc   *core.Service
	nodes []*core.Node // nil until a Node lister successfully lists at least once
}

// NewDiscovery returns a Discovery with an empty candidate snapshot. Call Start to
// begin watching.
func NewDiscovery() *Discovery {
	d := &Discovery{}
	empty := []Candidate{}
	d.candidates.Store(&empty)
	return d
}

// Candidates returns the most recently computed candidate list. Safe for concurrent
// use; callers must not mutate the returned slice.
func (d *Discovery) Candidates() []Candidate {
	return *d.candidates.Load()
}

// Start begins watching the named Service in namespace, and -- if the
// traffic-manager's ServiceAccount can list/watch Nodes -- the cluster's Nodes, so
// that NodePort candidates can be resolved to addresses. It never blocks: the initial
// state is fetched synchronously (so the first Candidates() call after Start returns
// already reflects it in the common case), and both watches then continue in the
// background, retrying on failure, until ctx is done.
//
// Missing Node RBAC (expected for namespace-scoped installs) is not an error: it is
// logged once at info and NodePort Services simply advertise no candidates, per the
// documented degradation (the admin must set quicTunnel.externalHost explicitly).
func (d *Discovery) Start(ctx context.Context, namespace, serviceName string) {
	svcClient := k8sapi.GetK8sInterface(ctx).CoreV1().Services(namespace)
	if svc, err := svcClient.Get(ctx, serviceName, meta.GetOptions{}); err == nil {
		d.setService(ctx, svc)
	} else if !k8sErrors.IsNotFound(err) {
		clog.Debugf(ctx, "quic tunnel discovery: initial get of service %s.%s: %v", serviceName, namespace, err)
	}
	go d.watchServiceLoop(ctx, svcClient, serviceName)

	ok, err := k8sapi.CanI(ctx,
		&auth.ResourceAttributes{Verb: "list", Resource: "nodes"},
		&auth.ResourceAttributes{Verb: "watch", Resource: "nodes"})
	if err != nil || !ok {
		clog.Info(ctx, "quic tunnel discovery: no permission to list/watch nodes; "+
			"a NodePort quicTunnel.service.type will not be advertised unless "+
			"quicTunnel.externalHost is set explicitly")
		return
	}

	// Nodes are read through the manager's existing shared informer factory, the
	// same mechanism cluster.watchNodeSubnets uses: unlike the Services informer
	// (see watchService below), nothing else installs a destructive transform on
	// the shared Nodes informer, so sharing it is safe.
	nodeFactory := informer.GetK8sFactory(ctx, "")
	nodeController := nodeFactory.Core().V1().Nodes()
	nodeLister := nodeController.Lister()
	nodeInformer := nodeController.Informer()
	recomputeNodes := func() { d.onNodes(ctx, nodeLister) }
	_, _ = nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { recomputeNodes() },
		UpdateFunc: func(_, _ any) { recomputeNodes() },
		DeleteFunc: func(any) { recomputeNodes() },
	})
	nodeFactory.Start(ctx.Done())
	nodeFactory.WaitForCacheSync(ctx.Done())
	recomputeNodes()
}

// watchServiceLoop retries watchService, with a backoff, until ctx is done.
func (d *Discovery) watchServiceLoop(ctx context.Context, svcClient serviceGetterWatcher, serviceName string) {
	for ctx.Err() == nil {
		if err := d.watchService(ctx, svcClient, serviceName); err != nil {
			clog.Debugf(ctx, "quic tunnel discovery: service watch for %s: %v", serviceName, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(serviceWatchBackoff):
			}
		}
	}
}

// serviceGetterWatcher is the subset of typedCore.ServiceInterface watchService
// needs, narrowed so discovery_test.go can supply a fake without a full clientset.
type serviceGetterWatcher interface {
	Get(ctx context.Context, name string, opts meta.GetOptions) (*core.Service, error)
	Watch(ctx context.Context, opts meta.ListOptions) (watch.Interface, error)
}

// watchService runs one Watch on serviceName until it fails, is closed by the
// server, or ctx is done. It does not go through the manager's shared Services
// informer (cmd/traffic/cmd/manager/mutator/service_watcher.go's startServices,
// registered on the same shared informer whenever it watches this Service's
// namespace) because that informer's SetTransform zeroes Status on every object it
// caches -- exactly the field a LoadBalancer Service's ingress addresses live in.
// A periodic Get resync bounds how stale the snapshot can get if a watch event is
// ever silently dropped, the same tolerance state.watchNodeAgentPods uses for its
// own raw Pod watch and for the same reason.
func (d *Discovery) watchService(ctx context.Context, svcClient serviceGetterWatcher, serviceName string) error {
	opts := meta.ListOptions{FieldSelector: "metadata.name=" + serviceName}
	w, err := svcClient.Watch(ctx, opts)
	if err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	defer w.Stop()

	resync := time.NewTicker(serviceWatchResync)
	defer resync.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.ResultChan():
			if !ok {
				return errors.New("watch channel closed")
			}
			switch ev.Type {
			case watch.Added, watch.Modified:
				if svc, ok := ev.Object.(*core.Service); ok {
					d.setService(ctx, svc)
				}
			case watch.Deleted:
				d.setService(ctx, nil)
			case watch.Error:
				return fmt.Errorf("watch error event: %v", ev.Object)
			}
		case <-resync.C:
			svc, err := svcClient.Get(ctx, serviceName, meta.GetOptions{})
			if err != nil {
				if k8sErrors.IsNotFound(err) {
					d.setService(ctx, nil)
					continue
				}
				return fmt.Errorf("resync get: %w", err)
			}
			d.setService(ctx, svc)
		}
	}
}

func (d *Discovery) setService(ctx context.Context, svc *core.Service) {
	d.mu.Lock()
	d.svc = svc
	d.mu.Unlock()
	d.recompute(ctx)
}

func (d *Discovery) onNodes(ctx context.Context, lister listersCore.NodeLister) {
	nodes, err := lister.List(labels.Everything())
	if err != nil {
		clog.Errorf(ctx, "quic tunnel discovery: unable to list nodes: %v", err)
		return
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	d.mu.Lock()
	d.nodes = nodes
	d.mu.Unlock()
	d.recompute(ctx)
}

// recompute derives and publishes a fresh snapshot. The store happens under mu so
// that two concurrent recomputes cannot publish out of input order and leave a stale
// snapshot in place until the next resync.
func (d *Discovery) recompute(ctx context.Context) {
	d.mu.Lock()
	cands := candidatesForService(d.svc, d.nodes)
	d.candidates.Store(&cands)
	d.mu.Unlock()
	clog.Debugf(ctx, "quic tunnel discovery: %d candidate(s)", len(cands))
}

// candidatesForService derives the ordered candidate list for svc, a pure function of
// its Service type so it can be unit tested against fake objects with no client or
// watch involved. nodes is only consulted for a NodePort Service and must already be
// sorted by name; a nil nodes (node access unavailable, or not yet listed) yields no
// candidates for a NodePort Service.
func candidatesForService(svc *core.Service, nodes []*core.Node) []Candidate {
	if svc == nil || len(svc.Spec.Ports) == 0 {
		return nil
	}
	var cands []Candidate
	switch svc.Spec.Type {
	case core.ServiceTypeLoadBalancer:
		port := svc.Spec.Ports[0].Port
		for _, ing := range svc.Status.LoadBalancer.Ingress {
			host := ing.IP
			if host == "" {
				host = ing.Hostname
			}
			if host == "" {
				continue
			}
			cands = append(cands, Candidate{Host: host, Port: port})
		}
	case core.ServiceTypeNodePort:
		nodePort := svc.Spec.Ports[0].NodePort
		if nodePort == 0 {
			return nil
		}
		for _, node := range nodes {
			if host := preferredNodeAddress(node); host != "" {
				cands = append(cands, Candidate{Host: host, Port: nodePort})
			}
		}
	default:
		// ClusterIP (or any other type): not reachable from outside the cluster,
		// so there is nothing to advertise.
	}
	if len(cands) > maxCandidates {
		cands = cands[:maxCandidates]
	}
	return cands
}

// preferredNodeAddress returns a Node's ExternalIP if it has one, else its
// InternalIP, else "" (the Node is skipped).
func preferredNodeAddress(node *core.Node) string {
	var internal string
	for _, addr := range node.Status.Addresses {
		switch addr.Type {
		case core.NodeExternalIP:
			return addr.Address
		case core.NodeInternalIP:
			if internal == "" {
				internal = addr.Address
			}
		}
	}
	return internal
}
