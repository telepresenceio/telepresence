package common

import "github.com/telepresenceio/telepresence/rpc/v2/manager"

func ModeToString(mode *manager.Mode) string {
	switch mode {
	case manager.Mode_MODE_SINGLE.Enum():
		return "single-user"
	case manager.Mode_MODE_TEAM.Enum():
		return "team"
	default:
		return "unknown"
	}
}
