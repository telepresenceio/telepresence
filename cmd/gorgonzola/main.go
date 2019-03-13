package main

import (
	"context"
	"fmt"
	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/datawire/teleproxy/pkg/watt"
	"github.com/spf13/cobra"
	"net/http"
	"os"
	"sync"
	"time"
)

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
	rootCmd.Flags().StringSliceVarP(&initialSources, "source", "s", []string{}, "configure an initial static source")
	rootCmd.Flags().IntVarP(&port, "port", "p", 7000, "configure the snapshot server port")
}

func makeWatchman(staticSources []string, sources <-chan []string) func(p *supervisor.Process) error {
	return func(p *supervisor.Process) error {
		fmt.Println("configuring static sources")
		for _, s := range staticSources {
			fmt.Printf("Setting up watch for %s\n", s)
		}

		fmt.Println("watching for dynamic sources")
		for {
			select {
			case source := <-sources:
				for _, s := range source {
					fmt.Printf("Setting up watch for %s\n", s)
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
				req.snapshot <- "THIS IS A SNAPSHOT"
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
	// 1. construct an initial list of things to watch
	// 2. feed them to the watch controller
	sourcesChan := make(chan []string)
	recordsChan := make(chan []string)

	getSnapshotChan := make(chan *getSnapshotRequest)

	fmt.Println(initialSources)

	ctx := context.Background()

	s := supervisor.WithContext(ctx)
	s.Supervise(&supervisor.Worker{
		Name:  "watchman",
		Work:  makeWatchman(initialSources, sourcesChan),
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

	cwm := &watt.ConsulServiceNodeWatchMaker{
		Service:     "foo",
		Datacenter:  "dc1",
		OnlyHealthy: true,
	}

	cwmFunc, _ := cwm.Make(recordsChan)
	s.Supervise(&supervisor.Worker{
		Name:  cwm.ID(),
		Work:  cwmFunc,
		Retry: false,
	})

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
				w.Write([]byte(res))
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
