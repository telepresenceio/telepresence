package a8rcloud

import (
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// API key descriptions to use when requesting API keys from Ambassador Cloud.
const (
	KeyDescWorkstation    = "telepresence:workstation"
	KeyDescTrafficManager = "telepresence:traffic-manager"

	// Not quite a real API key description, but the bogus name that we catalog the key under
	// when the user explicitly uses --apikey to pass us a key to use in order to create the
	// other keys.
	KeyDescRoot = ""
)

func KeyDescAgent(spec *manager.InterceptSpec) string {
	return "telepresence:agent-" + spec.Mechanism
}
