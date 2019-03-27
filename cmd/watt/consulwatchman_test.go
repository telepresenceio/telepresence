package main

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/datawire/teleproxy/pkg/consulwatch"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v2"
)

var standardTimeout = 10 * time.Second

var RegularConfigMap = `---
apiVersion: v1
kind: ConfigMap
metadata:
  name: %q
  namespace: %q
data:
  foo: bar
`

var ConsulResolverConfigMapTemplate = `---
apiVersion: v1
kind: ConfigMap
metadata:
  name: %q
  namespace: %q
  annotations:
    "getambassador.io/consul-resolver": "true"
data:
  service: %q
  datacenter: %q
  consulAddress: "127.0.0.1"
`

type consulwatchmanIsolator struct {
	aggregatorToWatchmanCh        chan []k8s.Resource
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

	// We always get blasted a big ol' slice of consistent state from Kubernetes.
	iso.aggregatorToWatchmanCh <- []k8s.Resource{
		CreateConsulResolverConfigMapFromTemplate("foo", "default", "foo-in-consul", "dc1"),
		CreateConsulResolverConfigMapFromTemplate("bar", "default", "bar-in-consul", "dc1"),
		CreateConsulResolverConfigMapFromTemplate("baz", "default", "baz-in-consul", "dc1"),
		CreateConfigMap("foo", "default"),
	}

	Eventually(t, standardTimeout, func() {
		assert.Len(t, iso.watchman.watched, 3)

		for k, worker := range iso.watchman.watched {
			assert.Equal(t, k, worker.Name)
		}
	})

	iso.aggregatorToWatchmanCh <- []k8s.Resource{
		CreateConsulResolverConfigMapFromTemplate("bar", "default", "bar-in-consul", "dc1"),
		CreateConsulResolverConfigMapFromTemplate("baz", "default", "baz-in-consul", "dc1"),
		CreateConfigMap("foo", "default"),
	}

	Eventually(t, standardTimeout, func() { assert.Len(t, iso.watchman.watched, 2) })
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
		aggregatorToWatchmanCh: make(chan []k8s.Resource),

		// we need to create buffered channels for outputs because
		// nothing is asynchronously reading them in the test
		consulEndpointsToAggregatorCh: make(chan consulwatch.Endpoints, 100),

		// for signaling when the isolator is done
		done: make(chan struct{}),
	}

	iso.watchman = &consulwatchman{
		WatchMaker:                &NOOPWatchMaker{},
		watchesCh:                 iso.aggregatorToWatchmanCh,
		consulEndpointsAggregator: iso.consulEndpointsToAggregatorCh,
		watched:                   map[string]*supervisor.Worker{},
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

type NOOPWatchMaker struct {
	errorBeforeCreate bool
}

func (m *NOOPWatchMaker) MakeWatch(r k8s.Resource, aggregatorCh chan<- consulwatch.Endpoints) (*supervisor.Worker, error) {
	if m.errorBeforeCreate {
		return nil, fmt.Errorf("failed to create watch (errorBeforeCreate: %t)", m.errorBeforeCreate)
	}

	return &supervisor.Worker{
		Name: fmt.Sprintf("%s|%s|%s", r.Data()["consulAddress"], r.Data()["service"], r.Data()["datacenter"]),
		Work: func(p *supervisor.Process) error {
			//<-p.Shutdown()
			//time.Sleep(500 * time.Millisecond)
			return nil
		},
		Retry: false,
	}, nil
}

func CreateConfigMap(name, namespace string) k8s.Resource {
	stringResource := fmt.Sprintf(RegularConfigMap, name, namespace)
	yamlDec := yaml.NewDecoder(bytes.NewReader([]byte(stringResource)))
	raw := make(map[interface{}]interface{})
	err := yamlDec.Decode(raw)
	if err != nil {
		fmt.Println(err)
	}

	return k8s.NewResourceFromYaml(raw)
}

func CreateConsulResolverConfigMapFromTemplate(name, namespace, consulService, consulDatacenter string) k8s.Resource {
	stringResource := fmt.Sprintf(ConsulResolverConfigMapTemplate, name, namespace, consulService, consulDatacenter)
	yamlDec := yaml.NewDecoder(bytes.NewReader([]byte(stringResource)))
	raw := make(map[interface{}]interface{})
	err := yamlDec.Decode(raw)
	if err != nil {
		fmt.Println(err)
	}

	return k8s.NewResourceFromYaml(raw)
}
