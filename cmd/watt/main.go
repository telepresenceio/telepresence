package main

import (
	"context"
	"fmt"
	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/datawire/teleproxy/pkg/watt"
	"github.com/spf13/cobra"
	"net/http"
	"os"
	"strings"
	"sync"
)

var kubernetesNamespace string
var initialSources = make([]string, 0)
var port int

var rootCmd = &cobra.Command{
	Use:              "watt",
	Short:            "watt",
	Long:             "watt - watch all the things",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {},
	Run:              runWatt,
}

type getSnapshotRequest struct {
	snapshot chan string
}

func init() {
	rootCmd.Flags().StringVarP(&kubernetesNamespace, "namespace", "", "", "namespace to watch (default: all)")
	rootCmd.Flags().StringSliceVarP(&initialSources, "source", "s", []string{}, "configure an initial static source")
	rootCmd.Flags().IntVarP(&port, "port", "p", 7000, "configure the snapshot server port")
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
	assembler chan<- []k8s.Resource) func(p *supervisor.Process) error {

	return func(p *supervisor.Process) error {
		kubeWatcher := k8s.NewClient(nil).Watcher()

		for _, kind := range kinds {
			p.Logf("adding watch for %q", kind)
			err := kubeWatcher.WatchNamespace(namespace, kind, func(watcher *k8s.Watcher) {
				k := kinds
				p.Logf("change in watched resources")

				resources := make([]k8s.Resource, 0)
				for _, kind := range k {
					resources = append(resources, watcher.List(kind)...)
				}

				watchman <- resources
				assembler <- resources
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

func makeConsulWatcher(config k8s.Resource, assembler chan<- []k8s.Resource) (string, *supervisor.Worker) {
	data := config.Data()
	cwm := &watt.ConsulServiceNodeWatchMaker{
		Service:     data["service"].(string),
		Datacenter:  data["datacenter"].(string),
		OnlyHealthy: true,
	}

	cwmFunc, _ := cwm.Make(assembler)
	return cwm.ID(), &supervisor.Worker{
		Name:  cwm.ID(),
		Work:  cwmFunc,
		Retry: false,
	}
}

func makeWatchman(resourcesChan <-chan []k8s.Resource, assembler chan<- []k8s.Resource) func(p *supervisor.Process) error {
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
						ID, worker := makeConsulWatcher(r, assembler)
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

func makeAssembler(getSnapshotCh <-chan *getSnapshotRequest, recordsCh <-chan []k8s.Resource) func(p *supervisor.Process) error {
	return func(p *supervisor.Process) error {
		p.Ready()
		snapshots := make([][]k8s.Resource, 0)

		for {
			select {
			case req := <-getSnapshotCh:
				p.Logf("returning snapshot")
				if len(snapshots) != 0 {
					snapshotBytes, _ := k8s.MarshalResources(snapshots[len(snapshots)-1])
					req.snapshot <- string(snapshotBytes)
				} else {
					req.snapshot <- ""
				}

			case resources := <-recordsCh:
				p.Logf("creating snapshot")
				snapshots = append(snapshots, resources)
				if len(snapshots) > 10 {
					snapshots = snapshots[1:]
				}
			case <-p.Shutdown():
				return nil
			}
		}
	}
}

func runWatt(_ *cobra.Command, _ []string) {
	fmt.Println("Watt - Watch All The Things! Starting...")

	// 1. construct an initial list of things to watch
	// 2. feed them to the watch controller
	watchman := make(chan []k8s.Resource)
	assembler := make(chan []k8s.Resource)

	fmt.Println(initialSources)

	ctx := context.Background()

	s := supervisor.WithContext(ctx)
	s.Supervise(&supervisor.Worker{
		Name:     "kubewatcher",
		Work:     makeKubeWatcher(kubernetesNamespace, initialSources, watchman, assembler),
		Requires: []string{"watchman"},
		Retry:    false,
	})

	s.Supervise(&supervisor.Worker{
		Name:     "watchman",
		Work:     makeWatchman(watchman, assembler),
		Requires: []string{"assembler"},
		Retry:    false,
	})

	getSnapshotChan := make(chan *getSnapshotRequest)
	s.Supervise(&supervisor.Worker{
		Name:  "assembler",
		Work:  makeAssembler(getSnapshotChan, assembler),
		Retry: false,
	})

	s.Supervise(&supervisor.Worker{
		Name:     "snapshot server",
		Requires: []string{"assembler"},
		Work: func(p *supervisor.Process) error {
			http.HandleFunc("/snapshot", func(w http.ResponseWriter, r *http.Request) {
				wg := &sync.WaitGroup{}
				snapshot := make(chan string)
				getSnapshotChan <- &getSnapshotRequest{snapshot: snapshot}

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
