package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type Mode int32

const (
	ModeUnspecified Mode = iota
	ModeSingle
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

func (m Mode) String() string {
	switch m {
	case 0:
		return "unspecified"
	case 1:
		return "single"
	case 2:
		return "team"
	}
	return "INVALID_MODE"
}

type TrafficManager struct {
	Mode Mode
}
