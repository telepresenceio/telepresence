package config

import (
	"fmt"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Mode uint8

const (
	ModeSingle Mode = iota
	ModeTeam
)

func (m *Mode) UnmarshalYAML(value *yaml.Node) error {
	switch strings.ToLower(value.Value) {
	case "single":
		*m = ModeSingle
	case "team":
		*m = ModeTeam
	default:
		return fmt.Errorf("invalid mode %s, must be 'team' or 'single'", value.Value)
	}
	return nil
}

type TrafficManager struct {
	Mode Mode

	sync.RWMutex `yaml:"-"`
}
