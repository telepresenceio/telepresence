package main

import (
	"context"
	"testing"
	"time"

	"github.com/ecodia/golang-awaitility/awaitility"
	"github.com/stretchr/testify/assert"

	"github.com/datawire/teleproxy/pkg/supervisor"
)

func TestAddAndRemoveKubernetesWatchers(t *testing.T) {
	iso := startKubewatchmanIsolator(t)
	defer iso.Stop()

	specs := []KubernetesWatchSpec{
		{Kind: "Service", Namespace: "", FieldSelector: "metadata.name=foo", LabelSelector: ""},
		{Kind: "Service", Namespace: "", FieldSelector: "metadata.name=bar", LabelSelector: ""},
		{Kind: "Service", Namespace: "", FieldSelector: "metadata.name=baz", LabelSelector: ""},
	}

	iso.aggregatorToWatchmanCh <- specs

	err := awaitility.Await(100*time.Millisecond, 1000*time.Millisecond, func() bool {
		return len(iso.watchman.watched) == len(specs)
	})

	if err != nil {
		t.Fatal(err)
	}

	assert.Len(t, iso.watchman.watched, len(specs))
	for k, worker := range iso.watchman.watched {
		assert.Equal(t, k, worker.Name)
	}

	specs = []KubernetesWatchSpec{
		{Kind: "Service", Namespace: "", FieldSelector: "metadata.name=foo", LabelSelector: ""},
		{Kind: "Service", Namespace: "", FieldSelector: "metadata.name=bar", LabelSelector: ""},
	}

	iso.aggregatorToWatchmanCh <- specs
	err = awaitility.Await(100*time.Millisecond, 1000*time.Millisecond, func() bool {
		return len(iso.watchman.watched) == len(specs)
	})

	if err != nil {
		t.Fatal(err)
	}

	assert.Len(t, iso.watchman.watched, len(specs))
	for k, worker := range iso.watchman.watched {
		assert.Equal(t, k, worker.Name)
	}

	specs = []KubernetesWatchSpec{
		{Kind: "Service", Namespace: "", FieldSelector: "metadata.name=foo", LabelSelector: ""},
	}

	iso.aggregatorToWatchmanCh <- specs
	err = awaitility.Await(100*time.Millisecond, 1000*time.Millisecond, func() bool {
		return len(iso.watchman.watched) == len(specs)
	})

	if err != nil {
		t.Fatal(err)
	}

	assert.Len(t, iso.watchman.watched, len(specs))
	for k, worker := range iso.watchman.watched {
		assert.Equal(t, k, worker.Name)
	}
}

type kubewatchmanIsolator struct {
	aggregatorToWatchmanCh          chan []KubernetesWatchSpec
	kubernetesResourcesToAggregator chan k8sEvent
	watchman                        *kubewatchman
	sup                             *supervisor.Supervisor
	done                            chan struct{}
	t                               *testing.T
	cancel                          context.CancelFunc
}

func startKubewatchmanIsolator(t *testing.T) *kubewatchmanIsolator {
	iso := newKubewatchmanIsolator(t)
	iso.Start()
	return iso
}

func (iso *kubewatchmanIsolator) Start() {
	go func() {
		errs := iso.sup.Run()
		if len(errs) > 0 {
			iso.t.Errorf("unexpected errors: %v", errs)
		}
		close(iso.done)
	}()
}

func (iso *kubewatchmanIsolator) Stop() {
	iso.sup.Shutdown()
	iso.cancel()
	<-iso.done
}

func newKubewatchmanIsolator(t *testing.T) *kubewatchmanIsolator {
	iso := &kubewatchmanIsolator{
		aggregatorToWatchmanCh: make(chan []KubernetesWatchSpec),

		// we need to create buffered channels for outputs because
		// nothing is asynchronously reading them in the test
		kubernetesResourcesToAggregator: make(chan k8sEvent, 100),

		// for signaling when the isolator is done
		done: make(chan struct{}),
	}

	iso.watchman = &kubewatchman{
		WatchMaker: &MockWatchMaker{},
		in:         iso.aggregatorToWatchmanCh,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	iso.cancel = cancel
	iso.sup = supervisor.WithContext(ctx)
	iso.sup.Supervise(&supervisor.Worker{
		Name: "kubewatchman",
		Work: iso.watchman.Work,
	})
	return iso
}
