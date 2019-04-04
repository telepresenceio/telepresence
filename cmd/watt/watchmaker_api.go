package main

import (
	"github.com/datawire/teleproxy/pkg/supervisor"
)

type KubernetesWatchSpec struct {
	Kind          string
	Namespace     string
	FieldSelector string
	LabelSelector string
}

type ConsulWatchSpec struct {
	ConsulAddress string
	Datacenter    string
	ServiceName   string
}

// IKubernetesWatchMaker is an interface for KubernetesWatchMaker implementations. It mostly exists to facilitate the
// creation of testing mocks.
type IKubernetesWatchMaker interface {
	MakeKubernetesWatch(spec *KubernetesWatchSpec) (*supervisor.Worker, error)
}

// IConsulWatchMaker is an interface for ConsulWatchMaker implementations. It mostly exists to facilitate the
// creation of testing mocks.
type IConsulWatchMaker interface {
	MakeConsulWatch(spec *ConsulWatchSpec) (*supervisor.Worker, error)
}
