package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli"
)

type Mode manager.Mode

func (m *Mode) UnmarshalYAML(value *yaml.Node) error {
	switch strings.ToLower(value.Value) {
	case "single-user":
		*m = Mode(manager.Mode_MODE_SINGLE)
	case "team":
		*m = Mode(manager.Mode_MODE_TEAM)
	default:
		return fmt.Errorf("invalid mode %s, must be 'team' or 'single'", value.Value)
	}
	return nil
}

func (m Mode) MarshalYAML() (any, error) {
	switch m {
	case 1:
		return "single-user", nil
	case 2:
		return "team", nil
	}

	return "", fmt.Errorf("invalid mode: %d", m)
}

func (m Mode) String() string {
	mm := manager.Mode(m)
	return cli.ModeToString(&mm)
}

func (m Mode) IsTeam() bool {
	return m == Mode(manager.Mode_MODE_TEAM)
}

type TrafficManager struct {
	Mode Mode
}
