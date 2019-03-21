package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/datawire/consul-x/pkg/consulwatch"
	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/datawire/teleproxy/pkg/watt"
	"github.com/spf13/cobra"
)

var kubernetesNamespace string
var initialSources = make([]string, 0)
var notifyReceivers = make([]string, 0)
var port int

var rootCmd = &cobra.Command{
	Use:              "watt",
	Short:            "watt",
	Long:             "watt - watch all the things",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {},
	Run:              runWatt,
}

type invoker struct {
	snapshotCh <-chan string
	mux        sync.Mutex
	snapshots  map[int]string
	id         int
}

func (a *invoker) Work(p *supervisor.Process) error {
	p.Ready()
	for {
		select {
		case snapshot := <-a.snapshotCh:
			id := a.storeSnapshot(snapshot)
			// XXX: we should add garbage collection to
			// avoid running out of memory due to
			// snapshots
			a.invoke(id, snapshot)
		case <-p.Shutdown():
			p.Logf("shutdown initiated")
			return nil
		}
	}
}

func (a *invoker) storeSnapshot(snapshot string) int {
	a.mux.Lock()
	defer a.mux.Unlock()
	a.id += 1
	a.snapshots[a.id] = snapshot
	return a.id
}

func (a *invoker) getSnapshot(id int) string {
	a.mux.Lock()
	defer a.mux.Unlock()
	return a.snapshots[id]
}

func (a *invoker) invoke(id int, snapshot string) {
	fmt.Printf("invoke stub: %d, %s\n", id, snapshot)
}

type k8sEvent struct {
	kind      string
	resources []k8s.Resource
}

type aggregator struct {
	kubernetesEventsCh  <-chan k8sEvent
	kubernetesResources map[string][]k8s.Resource
	consulEndpointsCh   <-chan consulwatch.Endpoints
	consulEndpoints     map[string]consulwatch.Endpoints
	consulWatchesCh     chan<- []k8s.Resource
	snapshotCh          chan<- string
	bootstrapped        bool
}

func (a *aggregator) Work(p *supervisor.Process) error {
	p.Ready()

	for {
		a.maybeNotify(p)
		select {
		case event := <-a.kubernetesEventsCh:
			a.setKubernetesResources(event)
			watches := a.kubernetesResources["ConsulResolver"]
			a.consulWatchesCh <- watches
			a.maybeNotify(p)
		case endpoints := <-a.consulEndpointsCh:
			a.updateConsulEndpoints(endpoints)
			a.maybeNotify(p)
		}
	}
}

func (a *aggregator) isKubernetesBootstrapped(p *supervisor.Process) bool {
	// XXX: initialSources is a global
	return len(a.kubernetesResources) >= len(initialSources)
}

// Returns true if the current state of the world is complete. The
// kubernetes state of the world is always complete by definition
// because the kubernetes client provides that guarantee. The
// aggregate state of the world is complete when any consul services
// referenced by kubernetes have populated endpoint information (even
// if the value of the populated info is an empty set of endpoints).
func (a *aggregator) isComplete(p *supervisor.Process) bool {
	var requiredConsulServices []string

	for _, v := range a.kubernetesResources["ConsulResolver"] {
		// this is all kinds of type unsafe most likely
		requiredConsulServices = append(requiredConsulServices, v.Data()["service"].(string))
	}

	complete := true
	for _, name := range requiredConsulServices {
		_, ok := a.consulEndpoints[name]
		if !ok {
			p.Logf("waiting for endpoint info for %s", name)
			complete = false
		}
	}

	return complete
}

func (a *aggregator) maybeNotify(p *supervisor.Process) {
	if !a.isKubernetesBootstrapped(p) {
		return
	}

	if !a.bootstrapped && a.isComplete(p) {
		p.Logf("bootstrapped!")
		a.bootstrapped = true
	}

	if a.bootstrapped {
		a.snapshotCh <- a.generateSnapshot()
	}
}

func (a *aggregator) updateConsulEndpoints(endpoints consulwatch.Endpoints) {
	fmt.Println(endpoints)
	a.consulEndpoints[endpoints.Service] = endpoints
}

func (a *aggregator) setKubernetesResources(event k8sEvent) {
	a.kubernetesResources[event.kind] = event.resources
	if strings.ToLower(event.kind) == "configmap" {
		var resolvers []k8s.Resource
		for _, r := range event.resources {
			if isConsulResolver(r) {
				resolvers = append(resolvers, r)
			}
		}
		a.kubernetesResources["ConsulResolver"] = resolvers
	}
}

func (a *aggregator) generateSnapshot() string {
	return "generate snapshot stub\n"
}

type consulwatchman struct {
	watchesCh                 <-chan []k8s.Resource
	consulEndpointsAggregator chan<- consulwatch.Endpoints
	watched                   map[string]*supervisor.Worker
	ready                     bool
}

func (w *consulwatchman) Work(p *supervisor.Process) error {
	p.Ready()

	for {
		select {
		case resources := <-w.watchesCh:
			found := make(map[string]*supervisor.Worker)
			p.Logf("processing %d kubernetes resources", len(resources))
			for _, r := range resources {
				if !isConsulResolver(r) {
					panic(r)
				}
				worker, err := w.makeConsulWatcher(r)
				if err != nil {
					p.Logf("failed to create consul watch %v", err)
					continue
				}

				if _, exists := w.watched[worker.Name]; !exists {
					p.Logf("add consul watcher %s\n", worker.Name)
					p.Supervisor().Supervise(worker)
					w.watched[worker.Name] = worker
				}

				found[worker.Name] = worker
			}

			// purge the watches that no longer are needed because they did not come through the in the latest
			// report
			for k, worker := range w.watched {
				if _, exists := found[k]; !exists {
					p.Logf("remove consul watcher %s\n", k)
					worker.Shutdown()
				}
			}

			w.watched = found
		case <-p.Shutdown():
			p.Logf("shutdown initiated")
			return nil
		}
	}
}

func (w *consulwatchman) makeConsulWatcher(r k8s.Resource) (*supervisor.Worker, error) {
	data := r.Data()
	cwm := &watt.ConsulServiceNodeWatchMaker{
		ConsulAddress: data["consulAddress"].(string),
		Service:       data["service"].(string),
		Datacenter:    data["datacenter"].(string),
		OnlyHealthy:   true,
	}

	cwmFunc, err := cwm.Make(w.consulEndpointsAggregator)
	if err != nil {
		return nil, err
	}

	return &supervisor.Worker{
		Name:  cwm.ID(),
		Work:  cwmFunc,
		Retry: false,
	}, nil
}

type kubewatchman struct {
	namespace string
	kinds     []string
	notify    []chan<- k8sEvent
}

func fmtNamespace(ns string) string {
	if ns == "" {
		return "*"
	}

	return ns
}

func (w *kubewatchman) Work(p *supervisor.Process) error {
	kubeAPIWatcher := k8s.NewClient(nil).Watcher()

	for _, kind := range w.kinds {
		p.Logf("adding kubernetes watch for %q in namespace %q", kind, fmtNamespace(kubernetesNamespace))

		watcherFunc := func(ns, kind string) func(watcher *k8s.Watcher) {
			return func(watcher *k8s.Watcher) {
				resources := watcher.List(kind)
				p.Logf("found %d %q in namespace %q", len(resources), kind, fmtNamespace(ns))
				for _, n := range w.notify {
					n <- k8sEvent{kind: kind, resources: resources}
				}
				p.Logf("sent %q to %d receivers", kind, len(w.notify))
			}
		}

		err := kubeAPIWatcher.WatchNamespace(w.namespace, kind, watcherFunc(w.namespace, kind))

		if err != nil {
			return err
		}
	}

	kubeAPIWatcher.Start()
	p.Ready()

	for {
		select {
		case <-p.Shutdown():
			p.Logf("shutdown initiated")
			kubeAPIWatcher.Stop()
			return nil
		}
	}
}

func isConsulResolver(r k8s.Resource) bool {
	kind := strings.ToLower(r.Kind())
	switch kind {
	case "configmap":
		a := r.Metadata().Annotations()
		if _, ok := a["getambassador.io/consul-resolver"]; ok {
			return true
		}
	}

	return false
}

type apiServer struct {
	port    int
	invoker *invoker
}

func (s *apiServer) Work(p *supervisor.Process) error {
	http.HandleFunc("/snapshots/", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/snapshots/"))
		if err != nil {
			http.Error(w, "ID is not an integer", http.StatusBadRequest)
			return
		}

		snapshot := s.invoker.getSnapshot(id)

		if snapshot == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("content-type", "application/json")
		if _, err := w.Write([]byte(snapshot)); err != nil {
			p.Logf("write snapshot error: %v", err)
		}
	})

	listenHostAndPort := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", listenHostAndPort)
	if err != nil {
		return err
	}
	defer listener.Close()
	p.Ready()
	p.Logf("snapshot server listening on: %s", listenHostAndPort)
	srv := &http.Server{
		Addr: listenHostAndPort,
	}
	// launch an anonymous child worker to serve requests
	p.Go(func(p *supervisor.Process) error {
		return srv.Serve(listener)
	})

	<-p.Shutdown()
	return srv.Shutdown(p.Context())
}

func init() {
	rootCmd.Flags().StringVarP(&kubernetesNamespace, "namespace", "n", "", "namespace to watch (default: all)")
	rootCmd.Flags().StringSliceVarP(&initialSources, "source", "s", []string{}, "configure an initial static source")
	rootCmd.Flags().StringSliceVar(&notifyReceivers, "notify", []string{}, "invoke the program with the given arguments as a receiver")
	rootCmd.Flags().IntVarP(&port, "port", "p", 7000, "configure the snapshot server port")
}

func runWatt(cmd *cobra.Command, args []string) {
	log.Printf("starting watt...")

	// Kubernetes resource events flow along this channel from the
	// individaul k8s watches to the aggregator.
	kubewatchesToAggregatorCh := make(chan k8sEvent)

	// Consul endpoint information flows along this channel from
	// the individual consul watches to the aggregator.
	consulwatchesToAggregatorCh := make(chan consulwatch.Endpoints)

	// The aggregator sends the current consul resolver set to the
	// consul watch manager.
	aggregatorToConsulwatchmanCh := make(chan []k8s.Resource)

	// The aggregator generates snapshots and sends them to the
	// invoker along this channel.
	aggregatorToInvokerCh := make(chan string)

	aggregator := &aggregator{
		kubernetesEventsCh:  kubewatchesToAggregatorCh,
		consulEndpointsCh:   consulwatchesToAggregatorCh,
		consulWatchesCh:     aggregatorToConsulwatchmanCh,
		snapshotCh:          aggregatorToInvokerCh,
		kubernetesResources: make(map[string][]k8s.Resource),
		consulEndpoints:     make(map[string]consulwatch.Endpoints),
	}

	kubewatchman := kubewatchman{
		namespace: kubernetesNamespace,
		kinds:     initialSources,
		notify:    []chan<- k8sEvent{kubewatchesToAggregatorCh},
	}

	consulwatchman := consulwatchman{
		watchesCh:                 aggregatorToConsulwatchmanCh,
		consulEndpointsAggregator: consulwatchesToAggregatorCh,
		watched:                   make(map[string]*supervisor.Worker),
	}

	invoker := &invoker{
		snapshotCh: aggregatorToInvokerCh,
		snapshots:  make(map[int]string),
	}

	apiServer := &apiServer{
		port:    port,
		invoker: invoker,
	}

	ctx := context.Background()
	s := supervisor.WithContext(ctx)

	s.Supervise(&supervisor.Worker{
		Name: "kubewatchman",
		Work: kubewatchman.Work,
	})

	s.Supervise(&supervisor.Worker{
		Name: "consulwatchman",
		Work: consulwatchman.Work,
	})

	s.Supervise(&supervisor.Worker{
		Name: "aggregator",
		Work: aggregator.Work,
	})

	s.Supervise(&supervisor.Worker{
		Name: "invoker",
		Work: invoker.Work,
	})

	s.Supervise(&supervisor.Worker{
		Name: "api",
		Work: apiServer.Work,
	})

	if errs := s.Run(); len(errs) > 0 {
		for _, err := range errs {
			log.Println(err)
		}
		os.Exit(1)
	}
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatalln(err)
	}
}
