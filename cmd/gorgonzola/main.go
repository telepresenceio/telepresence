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
	"time"
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

type KubernetesSource struct {
	Namespace string
	Kind      string
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

// makeKubewatcher returns a function that sets up a series of watches to the Kubernetes API for different
// kubernetes resources. When a change occurs the function gets the state of all resources that are being watched
// and sends a message to the watchman and assembler channels for further processing.
func makeKubewatcher(namespace string,
	kinds []string,
	watchman chan<- []k8s.Resource,
	assembler chan<- []k8s.Resource) func(p *supervisor.Process) error {

	return func(p *supervisor.Process) error {
		kubeWatcher := k8s.NewClient(nil).Watcher()

		for _, kind := range kinds {
			err := kubeWatcher.WatchNamespace(namespace, kind, func(watcher *k8s.Watcher) {
				resources := make([]k8s.Resource, 0)
				for _, kind := range kinds {
					resources = append(resources, watcher.List(kind)...)
				}

				watchman <- resources
				assembler <- resources
			})

			if err != nil {
				return err
			}
		}

		kubeWatcher.Start()

		for {
			select {
			case <-p.Shutdown():
				kubeWatcher.Stop()
				return nil
			}
		}
	}
}

func makeConsulWatcher(config k8s.Resource, assembler chan<- []k8s.Resource) (string, *supervisor.Worker) {
	data := config.Data()

	fmt.Println(data)

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
	watched := make([]string, 0)

	return func(p *supervisor.Process) error {
		for {
			select {
			case resources := <-resourcesChan:
				for _, r := range resources {
					rType, _ := isAmbassadorConfiguration(r)
					if rType == "consul-resolver" {
						fmt.Println(r)

						ID, worker := makeConsulWatcher(r, assembler)

						fmt.Println(2)
						watched = append(watched, ID)

						fmt.Println(3)
						p.Supervisor().Supervise(worker)
					}
				}
			case <-p.Shutdown():
				return nil
			}
		}
	}
}

func makeAssembler(getSnapshotCh <-chan *getSnapshotRequest, recordsCh <-chan []string) func(p *supervisor.Process) error {
	return func(p *supervisor.Process) error {
		snapshots := make([]watt.Snapshot, 0)

		for {
			select {
			case req := <-getSnapshotCh:
				req.snapshot <- "THIS IS A SNAPSHOT" // ignore this for now.

			case records := <-recordsCh:
				fmt.Println(records)
				snapshots = append(snapshots)
				if len(snapshots) > 10 {
					snapshots = snapshots[1:]
				}
			case <-p.Shutdown():
				return nil
			}
		}
	}
}

func makeTicker(frequency time.Duration, work func()) func(p *supervisor.Process) error {
	return func(p *supervisor.Process) error {
		ticker := time.NewTicker(frequency).C
		for {
			select {
			case <-ticker:
				work()
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

	sourcesChan := make(chan []string)
	recordsChan := make(chan []string)

	getSnapshotChan := make(chan *getSnapshotRequest)

	fmt.Println(initialSources)

	ctx := context.Background()

	s := supervisor.WithContext(ctx)
	s.Supervise(&supervisor.Worker{
		Name:  "kubewatcher",
		Work:  makeKubewatcher(kubernetesNamespace, initialSources, watchman, assembler),
		Retry: false,
	})

	s.Supervise(&supervisor.Worker{
		Name:  "watchman",
		Work:  makeWatchman(watchman, assembler),
		Retry: false,
	})

	s.Supervise(&supervisor.Worker{
		Name:  "assembler",
		Work:  makeAssembler(getSnapshotChan, recordsChan),
		Retry: false,
	})

	s.Supervise(&supervisor.Worker{
		Name: "sim-dynamic",
		Work: makeTicker(1*time.Second, func() { sourcesChan <- []string{"dynamic"} }),
	})

	//cwm := &watt.ConsulServiceNodeWatchMaker{
	//	Service:     "foo",
	//	Datacenter:  "dc1",
	//	OnlyHealthy: true,
	//}
	//
	//cwmFunc, _ := cwm.Make(recordsChan)
	//s.Supervise(&supervisor.Worker{
	//	Name:  cwm.ID(),
	//	Work:  cwmFunc,
	//	Retry: false,
	//})

	s.Supervise(&supervisor.Worker{
		Name: "snapshot server",
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
