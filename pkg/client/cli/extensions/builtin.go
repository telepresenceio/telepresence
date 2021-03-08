package extensions

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

// builtinExtensions is a function instead of a would-be-const var because its result includes the
// CLI version number, which might not be initialized yet at init-time (esp. during `go test`).
func builtinExtensions(_ context.Context) map[string]ExtensionInfo {
	return map[string]ExtensionInfo{
		// Real extensions won't have a "/" in the extname, by putting one builtin extension names
		// we can avoid clashes.
		"/builtin/telepresence": {
			Image: "${TELEPRESENCE_REGISTRY}/tel2:" + strings.TrimPrefix(client.Version(), "v"),
			Mechanisms: map[string]MechanismInfo{
				"tcp": {},
			},
		},
		// FIXME(lukeshu): We shouldn't compile in the info about the Ambassador Smart Agent
		// extension, but we don't yet have an installer to install the extension file; so this
		// metadata here is fine in the mean-time.
		"/builtin/ambassador": {
			Image:          "grpc+https://${SYSTEMA_HOST}:${SYSTEMA_PORT}", // XXX: not using net.JoinHostPort means that setting SYSTEMA_HOST to an IPv6 address won't work
			RequiresAPIKey: true,
			Mechanisms: map[string]MechanismInfo{
				"http": {
					Preference: 100,
					Flags: map[string]FlagInfo{
						"match": {
							Type:    "string-array",
							Default: json.RawMessage(`["auto"]`),
							Usage: `` +
								`Rather than intercepting all traffic service, only intercept traffic that matches this "HTTP2_HEADER=REGEXP" specifier. ` +
								`Instead of a "--match=HTTP2_HEADER=REGEXP" pair, you may say "--match=auto", which will automatically select a unique matcher for your intercept. ` +
								`Alternatively, you may say "--match=all", which is a no-op, but will inhibit the default "--match=auto" when you are logged in. ` +
								`If this flag is given multiple times, then it will only intercept traffic that matches *all* of the specifiers. ` +
								`(default "auto" if you are logged in with 'telepresence login', default "all" otherwise)`,
						},
					},
				},
			},
		},
	}
}
