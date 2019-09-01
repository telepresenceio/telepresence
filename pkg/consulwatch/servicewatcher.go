package consulwatch

import (
	"fmt"
	"log"
	"os"

	consulapi "github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/api/watch"
)

type ServiceWatcher struct {
	ServiceName string
	consul      *consulapi.Client
	logger      *log.Logger
	plan        *watch.Plan
}

func New(client *consulapi.Client, logger *log.Logger, datacenter string, service string, onlyHealthy bool) (*ServiceWatcher, error) {
	// NOTE plombardi@datawire.io, 2019-03-04
	// ======================================
	//
	// Technically we can watch on a specific "tag" for a Consul service. And in theory Consul via its CLI allows
	// watching multiple tags, however, the watch API only allows watching one specific tag which makes it kind of
	// useless unless you want to setup a watch per tag. The better approach and the reason the "tag" argument is not
	// supplied below is because it is conceptually simpler to post-process the array of Endpoints returned during a
	// watch and construct a map of tag names to an array of endpoints.
	plan, err := watch.Parse(map[string]interface{}{
		"type":        "service",
		"datacenter":  datacenter,
		"service":     service,
		"passingonly": onlyHealthy,
	})

	if err != nil {
		return nil, err
	}

	if logger == nil {
		logger = log.New(os.Stdout, "", log.LstdFlags)
	}

	return &ServiceWatcher{consul: client, logger: logger, ServiceName: service, plan: plan}, nil
}

func (w *ServiceWatcher) Watch(handler func(endpoints Endpoints, err error)) {
	w.plan.HybridHandler = func(val watch.BlockingParamVal, raw interface{}) {
		endpoints := Endpoints{Service: w.ServiceName, Endpoints: []Endpoint{}}

		if raw == nil {
			handler(endpoints, fmt.Errorf("unexpected empty/nil response from consul"))
			return
		}

		v, ok := raw.([]*consulapi.ServiceEntry)
		if !ok {
			handler(endpoints, fmt.Errorf("unexpected raw type expected=%T, actual=%T", []*consulapi.ServiceEntry{}, raw))
			return
		}

		endpoints.Endpoints = make([]Endpoint, 0)
		for _, item := range v {
			tags := make([]string, 0)
			if item.Service.Tags != nil {
				tags = item.Service.Tags
			}

			endpoints.Endpoints = append(endpoints.Endpoints, Endpoint{
				Service:  item.Service.Service,
				SystemID: fmt.Sprintf("consul::%s", item.Node.ID),
				ID:       item.Service.ID,
				Address:  item.Service.Address,
				Port:     item.Service.Port,
				Tags:     tags,
			})
		}

		handler(endpoints, nil)
	}
}

func (w *ServiceWatcher) Start() error {
	return w.plan.RunWithClientAndLogger(w.consul, w.logger)
}

func (w *ServiceWatcher) Stop() {
	w.plan.Stop()
}
