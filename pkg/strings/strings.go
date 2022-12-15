package strings

import "github.com/telepresenceio/telepresence/rpc/v2/manager"

func FromMode[M ~int32](mode M) string {
	switch mode {
	case M(manager.Mode_MODE_SINGLE):
		return "single-user"
	case M(manager.Mode_MODE_TEAM):
		return "team"
	default:
		return "unknown"
	}
}
