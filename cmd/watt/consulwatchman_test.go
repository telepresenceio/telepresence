package main

import (
	"context"
	"testing"
	"time"

	"github.com/ecodia/golang-awaitility/awaitility"
	"github.com/stretchr/testify/assert"

	"github.com/datawire/teleproxy/pkg/consulwatch"
	"github.com/datawire/teleproxy/pkg/supervisor"
)

type consulwatchmanIsolator struct {
	aggregatorToWatchmanCh        chan []ConsulWatchSpec
	consulEndpointsToAggregatorCh chan consulwatch.Endpoints
	watchman                      *consulwatchman
	sup                           *supervisor.Supervisor
	done                          chan struct{}
	t                             *testing.T
	cancel                        context.CancelFunc
}

func TestAddAndRemoveConsulWatchers(t *testing.T) {
	iso := startConsulwatchmanIsolator(t)
	defer iso.Stop()

	specs := []ConsulWatchSpec{
		{ConsulAddress: "127.0.0.1", ServiceName: "foo-in-consul", Datacenter: "dc1"},
		{ConsulAddress: "127.0.0.1", ServiceName: "bar-in-consul", Datacenter: "dc1"},
		{ConsulAddress: "127.0.0.1", ServiceName: "baz-in-consul", Datacenter: "dc1"},
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

	specs = []ConsulWatchSpec{
		{ConsulAddress: "127.0.0.1", ServiceName: "bar-in-consul", Datacenter: "dc1"},
		{ConsulAddress: "127.0.0.1", ServiceName: "baz-in-consul", Datacenter: "dc1"},
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

	specs = []ConsulWatchSpec{
		{ConsulAddress: "127.0.0.1", ServiceName: "bar-in-consul", Datacenter: "dc1"},
		{ConsulAddress: "127.0.0.1", ServiceName: "baz-in-consul", Datacenter: "dc1"},
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

func startConsulwatchmanIsolator(t *testing.T) *consulwatchmanIsolator {
	iso := newConsulwatchmanIsolator(t)
	iso.Start()
	return iso
}

func (iso *consulwatchmanIsolator) Start() {
	go func() {
		errs := iso.sup.Run()
		if len(errs) > 0 {
			iso.t.Errorf("unexpected errors: %v", errs)
		}
		close(iso.done)
	}()
}

func (iso *consulwatchmanIsolator) Stop() {
	iso.sup.Shutdown()
	iso.cancel()
	<-iso.done
}

func newConsulwatchmanIsolator(t *testing.T) *consulwatchmanIsolator {
	iso := &consulwatchmanIsolator{
		// by using zero length channels for inputs here, we can
		// control the total ordering of all inputs and therefore
		// intentionally trigger any order of events we want to test
		aggregatorToWatchmanCh: make(chan []ConsulWatchSpec),

		// we need to create buffered channels for outputs because
		// nothing is asynchronously reading them in the test
		consulEndpointsToAggregatorCh: make(chan consulwatch.Endpoints, 100),

		// for signaling when the isolator is done
		done: make(chan struct{}),
	}

	iso.watchman = &consulwatchman{
		WatchMaker: &ConsulWatchMaker{},
		watchesCh:  iso.aggregatorToWatchmanCh,
		watched:    map[string]*supervisor.Worker{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	iso.cancel = cancel
	iso.sup = supervisor.WithContext(ctx)
	iso.sup.Supervise(&supervisor.Worker{
		Name: "consulwatchman",
		Work: iso.watchman.Work,
	})
	return iso
}
