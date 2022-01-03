package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

// builtinExtensions is a function instead of a would-be-const var because its result includes the
// CLI version number, which might not be initialized yet at init-time (esp. during `go test`).
func builtinExtensions(ctx context.Context) map[string]ExtensionInfo {
	cfg := client.GetConfig(ctx)
	registry := cfg.Images.Registry
	cloud := cfg.Cloud
	version := strings.TrimPrefix(client.Version(), "v")
	image := fmt.Sprintf("%s/tel2:%s", registry, version)
	// XXX: not using net.JoinHostPort means that setting cloud.SystemaHost to an IPv6 address won't work
	extImage := fmt.Sprintf("grpc+https://%s:%s", cloud.SystemaHost, cloud.SystemaPort)
	return map[string]ExtensionInfo{
		// Real extensions won't have a "/" in the extname, by putting one builtin extension names
		// we can avoid clashes.
		"/builtin/telepresence": {
			Image: image,
			Mechanisms: map[string]MechanismInfo{
				"tcp": {},
			},
		},
		// FIXME(lukeshu): We shouldn't compile in the info about the Ambassador Smart Agent
		// extension, but we don't yet have an installer to install the extension file; so this
		// metadata here is fine in the mean-time.
		"/builtin/ambassador": {
			Image:                   extImage,
			RequiresAPIKeyOrLicense: true,
			Mechanisms: map[string]MechanismInfo{
				"http": {
					Preference: 100,
					Flags: map[string]FlagInfo{
						"match": {
							Type:    "string-array",
							Default: json.RawMessage(`["auto"]`),
							Usage: `` +
								`Rather than intercepting all traffic service, only intercept traffic that matches this "HTTP2_HEADER=REGEXP" specifier. ` +
								`Instead of a "--http-match=HTTP2_HEADER=REGEXP" pair, you may say "--http-match=auto", which will automatically select a unique matcher for your intercept. ` +
								`Alternatively, you may say "--http-match=all", which is a no-op, but will inhibit the default "--http-match=auto" when you are logged in. ` +
								`If this flag is given multiple times, then it will only intercept traffic that matches *all* of the specifiers. ` +
								`(default "auto" if you are logged in with 'telepresence login', default "all" otherwise)`,
						},
						"protocol": {
							Type: "string",
							Usage: `` +
								`Supported protocols are ` +
								`"http" (Plaintext HTTP/1.1), ` +
								`"http2" (Plaintext HTTP/2), ` +
								`"tls" (TLS Encrypted data)`,
						},
					},
				},
			},
		},
	}
}
