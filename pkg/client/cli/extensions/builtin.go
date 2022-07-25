package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/blang/semver"
	"github.com/spf13/pflag"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
)

func inArray(needle string, haystack []string) bool {
	for _, straw := range haystack {
		if needle == straw {
			return true
		}
	}
	return false
}

// builtinExtensions is a function instead of a would-be-const var because its result includes the
// CLI version number, which might not be initialized yet at init-time (esp. during `go test`).
func builtinExtensions(ctx context.Context) map[string]ExtensionInfo {
	cfg := client.GetConfig(ctx)
	registry := cfg.Images.Registry(ctx)
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
							Type:       "stringArray",
							Default:    json.RawMessage(`[]`),
							Usage:      "",
							Deprecated: "use --http-header",
						},
						"header": {
							Type:    "stringArray",
							Default: json.RawMessage(`["auto"]`),
							Usage: `` +
								`Only intercept traffic that matches this "HTTP2_HEADER=REGEXP" specifier. ` +
								`Instead of a "--http-header=HTTP2_HEADER=REGEXP" pair, you may say "--http-header=auto", which will automatically select a unique matcher for your intercept. ` +
								`Alternatively, you may say "--http-header=all", which is a no-op, but will inhibit the default "--http-header=auto" when you are logged in. ` +
								`If this flag is given multiple times, then it will only intercept traffic that matches *all* of the specifiers. ` +
								`(default "auto" if you are logged in with 'telepresence login', default "all" otherwise)`,
						},
						"path-equal": {
							Type:  "string",
							Usage: `Only intercept traffic with paths that are exactly equal to this path once the query string is removed`,
						},
						"path-prefix": {
							Type:  "string",
							Usage: `Only intercept traffic with paths beginning with this prefix`,
						},
						"path-regex": {
							Type:  "string",
							Usage: `Only intercept traffic with paths that are entirely matched by this regular expression once the query string is removed`,
						},
						"meta": {
							Type: "stringArray",
							Usage: `` +
								`Associates key=value pairs with the intercept that can later be retrieved using the Telepresence API service`,
						},
						"plaintext": {
							Type: "bool",
							Usage: `` +
								`Use plaintext format when communicating with the interceptor process on the local workstation. Only ` +
								`meaningful when intercepting workloads annotated with "getambassador.io/inject-originating-tls-secret" ` +
								`to prevent that TLS is used during intercepts`,
						},
					},
					MakeArgsCompatible: func(args *pflag.FlagSet, image string) (*pflag.FlagSet, error) {
						var agentVer *semver.Version
						if cp := strings.LastIndexByte(image, ':'); cp > 0 {
							if v, err := semver.Parse(image[cp+1:]); err == nil {
								agentVer = &v
							}
						}
						// Concat all --match flags (renamed to --header) with --header flags
						if hs, _ := args.GetStringArray("match"); len(hs) > 0 {
							if args.Changed("header") {
								_hs, _ := args.GetStringArray("header")
								hs = append(hs, _hs...)
							}
							flagType, _ := cliutil.TypeFromString("stringArray")
							var err error
							args.Lookup("header").Value, err = flagType.NewFlagValueFromJson(hs)
							if err != nil {
								return nil, err
							}
							args.Lookup("match").Value, err = flagType.NewFlagValueFromJson([]string{})
							if err != nil {
								return nil, err
							}
						}
						if agentVer != nil && agentVer.LE(semver.MustParse("1.11.8")) {
							// Swap "header" and "match"
							header := args.Lookup("header")
							match := args.Lookup("match")
							header.Value, match.Value = match.Value, header.Value
							// Check that too new of flags aren't being used.
							blacklist := []string{
								"meta",
								"path-equal",
								"path-prefix",
								"path-regex",
							}
							if agentVer.LE(semver.MustParse("1.11.7")) {
								blacklist = append(blacklist, "plaintext")
							}
							for _, ma := range blacklist {
								flag := args.Lookup(ma)
								if flag.Value.String() != flag.DefValue {
									return nil, errcat.User.New("--http-" + ma)
								}
							}
							newArgs := pflag.NewFlagSet("", pflag.ContinueOnError)
							args.VisitAll(func(flag *pflag.Flag) {
								if inArray(flag.Name, blacklist) {
									return
								}
								newArgs.AddFlag(flag)
							})
							args = newArgs
						}
						return args, nil
					},
				},
			},
		},
	}
}
