package strings

import "github.com/telepresenceio/telepresence/rpc/v2/manager"

func FromMode(mode manager.Mode) string {
	switch mode {
	case manager.Mode_MODE_SINGLE:
		return "single-user"
	case manager.Mode_MODE_TEAM:
		return "team"
	default:
		return "unknown"
	}
}
