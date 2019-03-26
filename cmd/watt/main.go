package main

import (
	"context"
	"log"
	"os"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"
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

func init() {
	rootCmd.Flags().StringVarP(&kubernetesNamespace, "namespace", "n", "", "namespace to watch (default: all)")
	rootCmd.Flags().StringSliceVarP(&initialSources, "source", "s", []string{}, "configure an initial static source")
	rootCmd.Flags().StringSliceVar(&notifyReceivers, "notify", []string{}, "invoke the program with the given arguments as a receiver")
	rootCmd.Flags().IntVarP(&port, "port", "p", 7000, "configure the snapshot server port")
}

func runWatt(cmd *cobra.Command, args []string) {
	log.Printf("starting watt...")

	// The aggregator sends the current consul resolver set to the
	// consul watch manager.
	aggregatorToConsulwatchmanCh := make(chan []k8s.Resource)

	// The aggregator generates snapshots and sends them to the
	// invoker along this channel.
	aggregatorToInvokerCh := make(chan string)

	aggregator := NewAggregator(aggregatorToInvokerCh, aggregatorToConsulwatchmanCh, initialSources)

	kubewatchman := kubewatchman{
		namespace: kubernetesNamespace,
		kinds:     initialSources,
		notify:    []chan<- k8sEvent{aggregator.KubernetesEvents},
	}

	consulwatchman := consulwatchman{
		WatchMaker:                &ConsulWatchMaker{},
		watchesCh:                 aggregatorToConsulwatchmanCh,
		consulEndpointsAggregator: aggregator.ConsulEndpoints,
		watched:                   make(map[string]*supervisor.Worker),
	}

	invoker := &invoker{
		snapshotCh:    aggregatorToInvokerCh,
		snapshots:     make(map[int]string),
		notify:        notifyReceivers,
		apiServerPort: port,
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
