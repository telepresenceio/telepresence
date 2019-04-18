package main

import (
	"fmt"

	"github.com/datawire/teleproxy/pkg/supervisor"
)

type WatchSet struct {
	KubernetesWatches []KubernetesWatchSpec `json:"kubernetes-watches"`
	ConsulWatches     []ConsulWatchSpec     `json:"consul-watches"`
}

type KubernetesWatchSpec struct {
	Kind          string `json:"kind"`
	Namespace     string `json:"namespace"`
	FieldSelector string `json:"field-selector"`
	LabelSelector string `json:"label-selector"`
}

func star(s string) string {
	if s == "" {
		return "*"
	} else {
		return s
	}
}

func (k KubernetesWatchSpec) WatchId() string {
	return fmt.Sprintf("%s|%s|%s|%s", k.Kind, star(k.Namespace), star(k.FieldSelector), star(k.LabelSelector))
}

type ConsulWatchSpec struct {
	Id            string `json:"id"`
	ConsulAddress string `json:"consul-address"`
	Datacenter    string `json:"datacenter"`
	ServiceName   string `json:"service-name"`
}

func (c ConsulWatchSpec) WatchId() string {
	return fmt.Sprintf("%s|%s|%s", c.ConsulAddress, c.Datacenter, c.ServiceName)
}

// IKubernetesWatchMaker is an interface for KubernetesWatchMaker implementations. It mostly exists to facilitate the
// creation of testing mocks.
type IKubernetesWatchMaker interface {
	MakeKubernetesWatch(spec KubernetesWatchSpec) (*supervisor.Worker, error)
}

// IConsulWatchMaker is an interface for ConsulWatchMaker implementations. It mostly exists to facilitate the
// creation of testing mocks.
type IConsulWatchMaker interface {
	MakeConsulWatch(spec ConsulWatchSpec) (*supervisor.Worker, error)
}
