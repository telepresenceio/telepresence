package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/limiter"
	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/spf13/cobra"
)

var kubernetesNamespace string
var initialSources = make([]string, 0)
var notifyReceivers = make([]string, 0)
var port int
var interval time.Duration

var rootCmd = &cobra.Command{
	Use:              "watt",
	Short:            "watt",
	Long:             "watt - watch all the things",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {},
	Run:              runWatt,
}

func init() {
	rootCmd.Flags().StringVarP(&kubernetesNamespace, "namespace", "n", "", "namespace to watch (default: all)")
	rootCmd.Flags().StringSliceVarP(&initialSources, "source", "s", []string{}, "configure an initial static source")
	rootCmd.Flags().StringSliceVar(&notifyReceivers, "notify", []string{},
		"invoke the program with the given arguments as a receiver")
	rootCmd.Flags().IntVarP(&port, "port", "p", 7000, "configure the snapshot server port")
	rootCmd.Flags().DurationVarP(&interval, "interval", "i", 250*time.Millisecond,
		"configure the rate limit interval")
}

func runWatt(cmd *cobra.Command, args []string) {
	if len(initialSources) == 0 {
		log.Fatalln("no initial sources configured")
	}

	kubeAPIWatcher := k8s.NewClient(nil).Watcher()
	for idx := range initialSources {
		initialSources[idx] = kubeAPIWatcher.Canonical(initialSources[idx])
	}

	log.Printf("starting watt...")

	// The aggregator sends the current consul resolver set to the
	// consul watch manager.
	aggregatorToConsulwatchmanCh := make(chan []ConsulWatch)

	invoker := NewInvoker(port, notifyReceivers, limiter.NewIntervalLimiter(interval))
	aggregator := NewAggregator(invoker.Snapshots, aggregatorToConsulwatchmanCh, initialSources)

	kubewatchman := kubewatchman{
		namespace:      kubernetesNamespace,
		kinds:          initialSources,
		kubeAPIWatcher: kubeAPIWatcher,
		notify:         []chan<- k8sEvent{aggregator.KubernetesEvents},
	}

	consulwatchman := consulwatchman{
		WatchMaker:                &ConsulWatchMaker{},
		watchesCh:                 aggregatorToConsulwatchmanCh,
		consulEndpointsAggregator: aggregator.ConsulEndpoints,
		watched:                   make(map[string]*supervisor.Worker),
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
