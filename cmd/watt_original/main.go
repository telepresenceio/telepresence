package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/datawire/consul-x/pkg/consulwatch"
	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/datawire/teleproxy/pkg/tpu"
	"github.com/datawire/teleproxy/pkg/watt"
	"github.com/spf13/cobra"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
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

type getSnapshotRequest struct {
	id       int
	snapshot chan string
}

func init() {
	rootCmd.Flags().StringVarP(&kubernetesNamespace, "namespace", "n", "", "namespace to watch (default: all)")
	rootCmd.Flags().StringSliceVarP(&initialSources, "source", "s", []string{}, "configure an initial static source")
	rootCmd.Flags().StringSliceVar(&notifyReceivers, "notify", []string{}, "invoke the program with the given arguments as a receiver")
	rootCmd.Flags().IntVarP(&port, "port", "p", 7000, "configure the snapshot server port")
}

func notify(program string, url string) {

}

// determine if we're dealing with a potential piece of Ambassador configuration. Right now that comes through in
// annotations of a Service. In the future it will likely be done via CRD. For this PoC I use a ConfigMap as pseudo-CRD.
func isAmbassadorConfiguration(r k8s.Resource) (string, bool) {
	kind := strings.ToLower(r.Kind())

	switch kind {
	case "service":
		// this is terribly hacky and not particularly important atm
		a := r.Metadata().Annotations()
		if _, ok := a["getambassador.io/config"]; ok {
			return "mapping", true
		}
	case "configmap":
		a := r.Metadata().Annotations()
		if _, ok := a["getambassador.io/consul-resolver"]; ok {
			return "consul-resolver", true
		}
	default:
		return "", false
	}

	return "", false
}

// this thing here would extract Ambassador annotation data from the metadata and then do something useful with it...
// let's pretend this is not important right now.
func extractAmbassadorAnnotation(r k8s.Resource) string {
	return ""
}

// makeKubeWatcher returns a function that sets up a series of watches to the Kubernetes API for different
// kubernetes resources. When a change occurs the function gets the state of all resources that are being watched
// and sends a message to the watchman and assembler channels for further processing.
func makeKubeWatcher(namespace string,
	kinds []string,
	watchman chan<- []k8s.Resource,
	kubernetesResources chan<- []k8s.Resource) func(p *supervisor.Process) error {

	return func(p *supervisor.Process) error {
		kubeWatcher := k8s.NewClient(nil).Watcher()

		for _, kind := range kinds {
			p.Logf("adding watch for %q", kind)
			k := kind
			err := kubeWatcher.WatchNamespace(namespace, kind, func(watcher *k8s.Watcher) {
				p.Logf("change in watched resources")

				resources := watcher.List(k)
				//for _, kind := range k {
				//	resources = append(resources, watcher.List(kind)...)
				//}

				watchman <- resources
				kubernetesResources <- resources
			})

			if err != nil {
				p.Logf("failed to add watch for %q", kind)
				return err
			}

			p.Logf("added watch for %q", kind)
		}

		kubeWatcher.Start()

		for {
			select {
			case <-p.Shutdown():
				p.Logf("shutdown initiated\n")
				kubeWatcher.Stop()
				return nil
			}
		}
	}
}

func makeConsulWatcher(config k8s.Resource, assembler chan<- consulwatch.Endpoints) (string, *supervisor.Worker, error) {
	data := config.Data()
	cwm := &watt.ConsulServiceNodeWatchMaker{
		Service:     data["service"].(string),
		Datacenter:  data["datacenter"].(string),
		OnlyHealthy: true,
	}

	cwmFunc, err := cwm.Make(assembler)
	if err != nil {
		return "", nil, err
	}

	return cwm.ID(), &supervisor.Worker{
		Name:  cwm.ID(),
		Work:  cwmFunc,
		Retry: false,
	}, nil
}

func makeWatchman(resourcesChan <-chan []k8s.Resource, endpoints chan<- consulwatch.Endpoints) func(p *supervisor.Process) error {
	return func(p *supervisor.Process) error {
		p.Ready()

		watched := make(map[string]*supervisor.Worker)

		for {
			select {
			case resources := <-resourcesChan:
				reported := make(map[string]*supervisor.Worker, 0)
				for _, r := range resources {
					rType, _ := isAmbassadorConfiguration(r)
					if rType == "consul-resolver" {
						ID, worker, err := makeConsulWatcher(r, endpoints)
						if err != nil {
							p.Logf("failed to create consul watch %v", err)
							continue
						}

						if _, exists := watched[ID]; !exists {
							p.Logf("add consul watcher %s\n", ID)
							p.Supervisor().Supervise(worker)
							watched[ID] = worker
						}

						reported[ID] = worker
					}
				}

				// purge the watches that no longer are needed because they did not come through the in the latest
				// report
				for k, worker := range watched {
					if _, exists := reported[k]; !exists {
						p.Logf("remove consul watcher %s\n", k)
						worker.Shutdown()
					}
				}

				watched = reported
			case <-p.Shutdown():
				return nil
			}
		}
	}
}

func makeAssembler(
	snapshotRequest <-chan *getSnapshotRequest,
	consulEndpoints <-chan consulwatch.Endpoints,
	kubernetesResources <-chan []k8s.Resource) func(p *supervisor.Process) error {

	return func(p *supervisor.Process) error {
		snapshotID := 0
		snapshots := make(map[int]string)

		s := watt.Snapshot{
			Consul: watt.ConsulSnapshot{
				Endpoints: make([]consulwatch.Endpoints, 0),
			},
			Kubernetes: []k8s.Resource{},
		}

		snapshotJSONBytes, err := json.MarshalIndent(s, "", "    ")
		if err != nil {
			p.Logf("error: failed to serialize snapshot")
		}

		snapshots[snapshotID] = string(snapshotJSONBytes)
		p.Ready()

		for {
			addedNewSnapshot := false

			select {
			case req := <-snapshotRequest:
				if _, found := snapshots[req.id]; found {
					req.snapshot <- snapshots[req.id]
				} else {
					req.snapshot <- snapshots[snapshotID]
				}
			case items := <-consulEndpoints:
				p.Logf("creating new snapshot with updated consul endpoints")
				latest := snapshots[len(snapshots)-1]

				snapshot := &watt.Snapshot{}
				err := json.Unmarshal([]byte(latest), snapshot)
				if err != nil {
					p.Logf("error: failed to unmarshal snapshot", err)
				}

				found := false
				for idx, service := range snapshot.Consul.Endpoints {
					if items.Service == service.Service {
						p.Logf("adding endpoints for known service")
						snapshot.Consul.Endpoints[idx] = items
						found = true
						break
					}
				}

				if !found {
					p.Logf("registering a new service and adding endpoints")
					snapshot.Consul.Endpoints = []consulwatch.Endpoints{items}
				}

				snapshotJSONBytes, err := json.MarshalIndent(snapshot, "", "    ")
				if err != nil {
					p.Logf("error: failed to serialize snapshot")
				}

				snapshotID += 1
				snapshots[snapshotID] = string(snapshotJSONBytes)
				addedNewSnapshot = true
			case items := <-kubernetesResources:
				p.Logf("creating new snapshot with updated kubernetes resources")
				latest := snapshots[len(snapshots)-1]

				snapshot := &watt.Snapshot{}
				err := json.Unmarshal([]byte(latest), snapshot)
				if err != nil {
					p.Logf("error: failed to unmarshal snapshot", err)
				}

				snapshot.Kubernetes = items
				snapshotJSONBytes, err := json.MarshalIndent(snapshot, "", "    ")
				if err != nil {
					p.Logf("error: failed to serialize snapshot")
				}

				snapshotID += 1
				snapshots[snapshotID] = string(snapshotJSONBytes)
				addedNewSnapshot = true
			case <-p.Shutdown():
				return nil
			}

			// purge the oldest record from the snapshot cache
			if len(snapshots) > 10 {
				delete(snapshots, snapshotID-10)
			}

			if addedNewSnapshot {
				for _, n := range notifyReceivers {
					p.Supervisor().Supervise(&supervisor.Worker{
						Name: fmt.Sprintf("notify-%s", n),
						Work: func(process *supervisor.Process) error {
							k := tpu.NewKeeper("SYNC", fmt.Sprintf("%s http://127.0.0.1:%d/snapshots/%d", n, port, snapshotID))
							k.Limit = 1
							k.Start()
							k.Wait()
							return nil
						},
					})
				}
			}
		}
	}
}

func runWatt(_ *cobra.Command, _ []string) {
	log.Println("Watt - Watch All The Things! Starting...")

	// 1. construct an initial list of things to watch
	// 2. feed them to the watch controller
	watchman := make(chan []k8s.Resource)

	kubernetesResourcesUpdate := make(chan []k8s.Resource)
	consulServiceEndpointsUpdate := make(chan consulwatch.Endpoints)

	if len(notifyReceivers) == 0 {
		notifyReceivers = append(notifyReceivers, "curl")
	}

	ctx := context.Background()

	s := supervisor.WithContext(ctx)
	s.Supervise(&supervisor.Worker{
		Name:     "kubewatcher",
		Work:     makeKubeWatcher(kubernetesNamespace, initialSources, watchman, kubernetesResourcesUpdate),
		Requires: []string{"watchman"},
		Retry:    false,
	})

	s.Supervise(&supervisor.Worker{
		Name:     "watchman",
		Work:     makeWatchman(watchman, consulServiceEndpointsUpdate),
		Requires: []string{"assembler"},
		Retry:    false,
	})

	getSnapshotChan := make(chan *getSnapshotRequest)
	s.Supervise(&supervisor.Worker{
		Name:  "assembler",
		Work:  makeAssembler(getSnapshotChan, consulServiceEndpointsUpdate, kubernetesResourcesUpdate),
		Retry: false,
	})

	s.Supervise(&supervisor.Worker{
		Name:     "snapshot server",
		Requires: []string{"assembler"},
		Work: func(p *supervisor.Process) error {
			http.HandleFunc("/snapshots/", func(w http.ResponseWriter, r *http.Request) {
				id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/snapshots/"))
				if err != nil {
					p.Logf("ID is not an integer")
				}

				wg := &sync.WaitGroup{}
				snapshot := make(chan string)
				getSnapshotChan <- &getSnapshotRequest{snapshot: snapshot, id: id}

				res := ""
				wg.Add(1)
				go func(replyChan chan string) {
					defer wg.Done()
					for v := range snapshot {
						res = v
						close(snapshot)
					}
				}(snapshot)

				wg.Wait()
				w.Header().Set("content-type", "application/json")
				if _, err := w.Write([]byte(res)); err != nil {
					p.Logf("write snapshot errored: %v", err)
				}
			})
			listenHostAndPort := fmt.Sprintf(":%d", port)
			p.Logf("snapshot server listening on: %s", listenHostAndPort)
			return http.ListenAndServe(listenHostAndPort, nil)
		},
	})

	if errs := s.Run(); len(errs) > 0 {
		for _, err := range errs {
			fmt.Println(err)
		}
	}
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
