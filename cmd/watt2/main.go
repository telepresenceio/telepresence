package main

import (
	"context"
	"fmt"
	"github.com/datawire/consul-x/pkg/consulwatch"
	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/datawire/teleproxy/pkg/watt"
	"github.com/spf13/cobra"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
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

type assembler struct {
	aggregatorNotifyCh <-chan struct{}
	snapshotRequestCh  <-chan getSnapshotRequest
	snapshots          map[int]string
}

func (a *assembler) Work(p *supervisor.Process) error {
	snapshots := make(map[int]string)
	snapshots[1] = `{"TODO": "Rafi!"}`

	p.Ready()
	for {
		select {
		case snapshotRequest := <-a.snapshotRequestCh:
			snapshotRequest.result <- getSnapshotResult{found: true, snapshot: snapshots[1]}
		case <-p.Shutdown():
			p.Logf("shutdown initiated")
			return nil
		}
	}
}

type bootstrappah struct {
	kubernetesResourcesCh <-chan []k8s.Resource
	consulEndpointsCh     <-chan consulwatch.Endpoints
	aggregator            *aggregator
}

func (b *bootstrappah) Work(p *supervisor.Process) error {
	p.Ready()
	requiredConsulServices := make(map[string]*consulwatch.Endpoints)

	var bootstrapped = false
	for {
		select {
		case resources := <-b.kubernetesResourcesCh:
			b.aggregator.setKubernetesResources(resources)
			for _, v := range b.aggregator.kubernetesResources["ConsulResolver"] {
				// this is all kinds of type unsafe most likely
				requiredConsulServices[v.Data()["service"].(string)] = nil
			}

			p.Logf("discovered %d consul resolver configurations", len(requiredConsulServices))
		case endpoints := <-b.consulEndpointsCh:
			requiredConsulServices[endpoints.Service] = &endpoints

			if !MapHasNilValues(requiredConsulServices) {
				bootstrapped = true
			}
		}

		p.Logf("bootstrapped!")
		if bootstrapped {
			break
		}
	}

	for _, v := range requiredConsulServices {
		b.aggregator.updateConsulEndpoints(*v)
	}

	p.Supervisor().Supervise(&supervisor.Worker{
		Name: "aggregator",
		Work: b.aggregator.Work,
	})

	return nil
}

func MapHasNilValues(m map[string]*consulwatch.Endpoints) bool {
	for _, v := range m {
		if v == nil {
			return true
		}
	}

	return false
}

type aggregator struct {
	kubernetesResourcesCh <-chan []k8s.Resource
	kubernetesResources   map[string][]k8s.Resource
	consulEndpointsCh     <-chan consulwatch.Endpoints
	consulEndpoints       map[string]consulwatch.Endpoints
	notifyAssemblerCh     chan<- struct{}
}

func (a *aggregator) Work(p *supervisor.Process) error {
	p.Ready()

	for {
		a.notifyAssemblerCh <- struct{}{}

		select {
		case resources := <-a.kubernetesResourcesCh:
			a.setKubernetesResources(resources)
		case endpoints := <-a.consulEndpointsCh:
			a.updateConsulEndpoints(endpoints)
		}
	}
}

func (a *aggregator) updateConsulEndpoints(endpoints consulwatch.Endpoints) {
	fmt.Println(endpoints)
	a.consulEndpoints[endpoints.Service] = endpoints
}

func (a *aggregator) setKubernetesResources(resources []k8s.Resource) {
	replacement := make(map[string][]k8s.Resource)
	for _, r := range resources {
		kind := r.Kind()

		if isConsulResolver(r) {
			kind = "ConsulResolver" // fake it till you make it baby; this will make lookups quicker
		}

		if _, exists := replacement[kind]; !exists {
			replacement[kind] = make([]k8s.Resource, 0)
		}

		replacement[kind] = append(replacement[kind], r)
	}

	a.kubernetesResources = replacement
}

type consulwatchman struct {
	kubernetes                <-chan []k8s.Resource
	consulEndpointsAggregator chan<- consulwatch.Endpoints
	watched                   map[string]*supervisor.Worker
	ready                     bool
}

func (w *consulwatchman) Work(p *supervisor.Process) error {
	p.Ready()

	for {
		select {
		case resources := <-w.kubernetes:
			found := make(map[string]*supervisor.Worker)
			p.Logf("processing %d kubernetes resources", len(resources))
			for _, r := range resources {
				if isConsulResolver(r) {
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
		Service:     data["service"].(string),
		Datacenter:  data["datacenter"].(string),
		OnlyHealthy: true,
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
	notify    []chan<- []k8s.Resource
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
					n <- resources
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
	port        int
	assemblerCh chan<- getSnapshotRequest
}

type getSnapshotResult struct {
	found    bool
	snapshot string
}

type getSnapshotRequest struct {
	id     int
	result chan<- getSnapshotResult
}

func (s *apiServer) Work(p *supervisor.Process) error {
	p.Ready()

	http.HandleFunc("/snapshots/", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/snapshots/"))
		if err != nil {
			p.Logf("ID is not an integer")
		}

		resultCh := make(chan getSnapshotResult)
		s.assemblerCh <- getSnapshotRequest{id: id, result: resultCh}

		result := <-resultCh
		if !result.found {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("content-type", "application/json")
		if _, err := w.Write([]byte(result.snapshot)); err != nil {
			p.Logf("write snapshot error: %v", err)
		}
	})

	listenHostAndPort := fmt.Sprintf(":%d", port)
	p.Logf("snapshot server listening on: %s", listenHostAndPort)
	return http.ListenAndServe(listenHostAndPort, nil)
}

func init() {
	rootCmd.Flags().StringVarP(&kubernetesNamespace, "namespace", "n", "", "namespace to watch (default: all)")
	rootCmd.Flags().StringSliceVarP(&initialSources, "source", "s", []string{}, "configure an initial static source")
	rootCmd.Flags().StringSliceVar(&notifyReceivers, "notify", []string{}, "invoke the program with the given arguments as a receiver")
	rootCmd.Flags().IntVarP(&port, "port", "p", 7000, "configure the snapshot server port")
}

func runWatt(cmd *cobra.Command, args []string) {
	log.Printf("starting watt...")

	consulWatchmanCh := make(chan []k8s.Resource)
	kubernetesResourceAggregatorCh := make(chan []k8s.Resource)
	consulEndpointsAggregatorCh := make(chan consulwatch.Endpoints)

	notifyAssembler := make(chan struct{})

	// bootstrapper waits for steady state then launches the aggregator
	bootstrappah := &bootstrappah{
		kubernetesResourcesCh: kubernetesResourceAggregatorCh,
		consulEndpointsCh:     consulEndpointsAggregatorCh,
		aggregator: &aggregator{
			kubernetesResourcesCh: kubernetesResourceAggregatorCh,
			kubernetesResources:   make(map[string][]k8s.Resource),
			consulEndpointsCh:     consulEndpointsAggregatorCh,
			consulEndpoints:       make(map[string]consulwatch.Endpoints),
			notifyAssemblerCh:     notifyAssembler,
		},
	}

	kubewatchman := kubewatchman{
		namespace: kubernetesNamespace,
		kinds:     initialSources,
		notify:    []chan<- []k8s.Resource{kubernetesResourceAggregatorCh, consulWatchmanCh},
	}

	consulwatchman := consulwatchman{
		kubernetes:                consulWatchmanCh,
		consulEndpointsAggregator: consulEndpointsAggregatorCh,
		watched:                   make(map[string]*supervisor.Worker),
	}

	snapshotRequestCh := make(chan getSnapshotRequest)
	assembler := &assembler{
		aggregatorNotifyCh: notifyAssembler,
		snapshotRequestCh:  snapshotRequestCh,
	}

	apiServer := &apiServer{
		port:        port,
		assemblerCh: snapshotRequestCh,
	}

	ctx := context.Background()
	s := supervisor.WithContext(ctx)

	s.Supervise(&supervisor.Worker{
		Name:     "kubewatchman",
		Work:     kubewatchman.Work,
		Requires: []string{"bootstrappah"},
	})

	s.Supervise(&supervisor.Worker{
		Name:     "consulwatchman",
		Work:     consulwatchman.Work,
		Requires: []string{"bootstrappah"},
	})

	s.Supervise(&supervisor.Worker{
		Name: "bootstrappah",
		Work: bootstrappah.Work,
	})

	s.Supervise(&supervisor.Worker{
		Name:     "assembler",
		Work:     assembler.Work,
		Requires: []string{"aggregator"},
	})

	s.Supervise(&supervisor.Worker{
		Name:     "api",
		Work:     apiServer.Work,
		Requires: []string{"assembler"},
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

//
//import (
//	"fmt"
//	"github.com/datawire/consul-x/pkg/consulwatch"
//	"github.com/datawire/teleproxy/pkg/k8s"
//	"github.com/datawire/teleproxy/pkg/supervisor"
//	"github.com/datawire/teleproxy/pkg/runWatt"
//	"strings"
//	"sync"
//)
//
//type kubeWatcherConfig struct {
//	Namespace string
//	Kinds     []string
//}
//
//type World struct {
//	ID          int
//	Kubernetes  KubernetesSnapshot
//	Consul      ConsulSnapshot
//	initialized bool
//}
//
//type ConsulSnapshot struct {
//	Endpoints []consulwatch.Endpoints
//}
//
//type KubernetesSnapshot struct {
//	Records []k8s.Resource
//}
//
//func (w *World) MarkInitialized() {
//	w.initialized = true
//}
//
//func isConsulResolverConfig(r k8s.Resource) bool {
//	kind := strings.ToLower(r.Kind())
//	switch kind {
//	case "configmap":
//		a := r.Metadata().Annotations()
//		if _, ok := a["getambassador.io/consul-resolver"]; ok {
//			return true
//		}
//	}
//
//	return false
//}
//
//// determine if we're dealing with a potential piece of Ambassador configuration. Right now that comes through in
//// annotations of a Service. In the future it will likely be done via CRD. For this PoC I use a ConfigMap as pseudo-CRD.
//func isAmbassadorConfiguration(r k8s.Resource) (string, bool) {
//	kind := strings.ToLower(r.Kind())
//
//	switch kind {
//	case "service":
//		// this is terribly hacky and not particularly important atm
//		a := r.Metadata().Annotations()
//		if _, ok := a["getambassador.io/config"]; ok {
//			return "mapping", true
//		}
//	case "configmap":
//		a := r.Metadata().Annotations()
//		if _, ok := a["getambassador.io/consul-resolver"]; ok {
//			return "consul-resolver", true
//		}
//	default:
//		return "", false
//	}
//
//	return "", false
//}
//
//func makeConsulWatcher(config k8s.Resource, aggregator chan<- consulwatch.Endpoints) (string, *supervisor.Worker, error) {
//	data := config.Data()
//	cwm := &runWatt.ConsulServiceNodeWatchMaker{
//		Service:     data["service"].(string),
//		Datacenter:  data["datacenter"].(string),
//		OnlyHealthy: true,
//	}
//
//	cwmFunc, err := cwm.Make(aggregator)
//	if err != nil {
//		return "", nil, err
//	}
//
//	return cwm.ID(), &supervisor.Worker{
//		Name:  cwm.ID(),
//		Work:  cwmFunc,
//		Retry: false,
//	}, nil
//}
//
//func updateConsulWatches(p *supervisor.Process, resources []k8s.Resource, watched map[string]supervisor.Worker) {
//	for _, r := range resources {
//		rType, _ := isAmbassadorConfiguration(r)
//		if rType == "consul-resolver" {
//			ID, worker, err := makeConsulWatcher(r, endpoints)
//			if err != nil {
//				p.Logf("failed to create consul watch %v", err)
//				continue
//			}
//
//			if _, exists := watched[ID]; !exists {
//				p.Logf("add consul watcher %s\n", ID)
//				p.Supervisor().Supervise(worker)
//				watched[ID] = worker
//			}
//
//			reported[ID] = worker
//		}
//	}
//
//	// purge the watches that no longer are needed because they did not come through the in the latest
//	// report
//	for k, worker := range watched {
//		if _, exists := reported[k]; !exists {
//			p.Logf("remove consul watcher %s\n", k)
//			worker.Shutdown()
//		}
//	}
//}
//
//func makeKubeWatcher(config *kubeWatcherConfig) func(p *supervisor.Process) error {
//	return func(p *supervisor.Process) error {
//		kubeWatcher := k8s.NewClient(nil).Watcher()
//
//		initialized := false
//		modifiedMutex := sync.Mutex{}
//		modified := false
//
//		for _, kind := range config.Kinds {
//			watcherFunc := func(w *k8s.Watcher) {
//				modifiedMutex.Lock()
//				defer modifiedMutex.Unlock()
//				if !modified {
//					modified = true
//				}
//			}
//
//			p.Logf("add watch for %q", kind)
//			err := kubeWatcher.WatchNamespace(config.Namespace, kind, watcherFunc)
//
//			if err != nil {
//				p.Logf("add watch for %q failed", kind)
//				return err
//			}
//		}
//
//		kubeWatcher.Start()
//
//		consulWatchers := make(map[string]supervisor.Worker)
//
//		consulEndpointUpdates := make(chan consulwatch.Endpoints)
//		consulEndpoints := make(map[string]consulwatch.Endpoints)
//
//		var firstEun = true
//		for {
//			consulKubernetesResources := make([]k8s.Resource, 0)
//
//			resources := make([]k8s.Resource, 0)
//			if modified {
//				modifiedMutex.Lock()
//				for _, kind := range config.Kinds {
//					resources = append(resources, kubeWatcher.List(kind)...)
//				}
//
//				modified = false
//				modifiedMutex.Unlock()
//			}
//
//			if len(resources) != 0 {
//				for _, r := range consulKubernetesResources {
//					if isConsulResolverConfig(r) {
//						consulKubernetesResources = append(consulKubernetesResources, r)
//					}
//				}
//			}
//
//			select {
//			case e := <-consulEndpointUpdates:
//				fmt.Printf(e.Service)
//			case <-p.Shutdown():
//				p.Logf("shutdown initiated")
//				kubeWatcher.Stop()
//				return nil
//			}
//		}
//
//
//		//for _, kind := range config.Kinds {
//		//	p.Logf("add watch for %q", kind)
//		//	err := kubeWatcher.WatchNamespace(config.Namespace, kind, watcherFunc)
//		//	if err != nil {
//		//		p.Logf("add watch for %q failed", kind)
//		//	}
//		//}
//		//
//		//for _, kind := range config.Kinds {
//		//	p.Logf("adding watch for %q", kind)
//		//	err := kubeWatcher.WatchNamespace(config.Namespace, kind, func(watcher *k8s.Watcher) {
//		//		p.Logf("change in watched resources")
//		//
//		//		resources := make([]k8s.Resource, 0)
//		//		for _, kind := range config.Kinds {
//		//			resources = append(resources, watcher.List(kind)...)
//		//		}
//		//
//		//		//watchman <- resources
//		//		//kubernetesResourcesCh <- resources
//		//	})
//		//
//		//	if err != nil {
//		//		p.Logf("failed to add watch for %q", kind)
//		//		return err
//		//	}
//		//
//		//	p.Logf("added watch for %q", kind)
//		//}
//	}
//}
//
//func main() {
//
//}
