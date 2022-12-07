package config

import (
	"fmt"
	"strings"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"gopkg.in/yaml.v3"
)

type Mode manager.Mode

func (m *Mode) UnmarshalYAML(value *yaml.Node) error {
	switch strings.ToLower(value.Value) {
	case "single":
		*m = Mode(manager.Mode_MODE_SINGLE)
	case "team":
		*m = Mode(manager.Mode_MODE_TEAM)
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

func (m Mode) IsTeam() bool {
	return m == Mode(manager.Mode_MODE_TEAM)
}

type TrafficManager struct {
	Mode Mode
}
