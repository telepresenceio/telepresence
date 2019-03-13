package watt

import (
	"github.com/datawire/teleproxy/pkg/supervisor"
)

type Config struct {
	Watches []WatcherMaker
}

type WatcherMaker interface {
	ID() string
	Make() (func(p *supervisor.Process) error, error)
}

type KubernetesResourceWatchMaker struct {
	Kind    string `json:""`
	Version string `json:""`
}
