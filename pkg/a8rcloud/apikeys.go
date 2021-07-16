package a8rcloud

import (
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// API key descriptions to use when requesting API keys from Ambassador Cloud.
const (
	KeyDescWorkstation    = "laptop"
	KeyDescTrafficManager = "manager"
)

func KeyDescAgent(spec *manager.InterceptSpec) string {
	return "agent-" + spec.Mechanism
}
