package a8rcloud

import (
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// API key descriptions to use when requesting API keys from Ambassador Cloud.
const (
	KeyDescWorkstation    = "telepresence:workstation"
	KeyDescTrafficManager = "telepresence:traffic-manager"
)

func KeyDescAgent(spec *manager.InterceptSpec) string {
	return "telepresence:agent-" + spec.Mechanism
}
