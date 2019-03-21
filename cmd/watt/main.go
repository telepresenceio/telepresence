package main

import (
	"context"
	"log"
	"os"

	"github.com/datawire/consul-x/pkg/consulwatch"
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
