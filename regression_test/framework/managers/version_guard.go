package managers

import (
	"strings"

	"github.com/blang/semver/v4"
)

// keyIntroduced maps a values.schema.yaml key the catalog's Default/
// Baseline spec (or a catalog entry a compat-core suite might use) emits to
// the telepresence-oss chart release it first appeared in. A dotted path
// (e.g. "client.nodeAgent") names a nested key. Sourced from `git log
// -S'<key>' -- charts/telepresence-oss/values.schema.yaml`.
//
// Only keys newer than the schema's own introduction need an entry: the
// schema file itself first appeared in v2.22.0 (f52b17220, "Add json-schema
// for the telepresence-oss Helm chart"), and every field Baseline sets that
// isn't listed here (image, agent.image, clientRbac, managerRbac, timeouts,
// namespaceSelector, logLevel) was already part of that initial schema.
//
//nolint:gochecknoglobals // static, checked-in compat table
var keyIntroduced = map[string]semver.Version{
	// usage.*: 7d8d4e27f "feat(usg): anonymous usage reporting subsystem".
	// Baseline always sets usage.enabled (no omitempty: "usage.enabled=false
	// must always reach the chart, whose own default is true"), so this is
	// the one guard every Default-spec compat-manager run actually needs.
	"usage": semver.MustParse("2.29.0"),
	// nodeAgent.*: 3b2bd7211 "Reap node-agent Jobs and gate the feature via
	// RBAC and Helm".
	"nodeAgent": semver.MustParse("2.30.0"),
	// client.nodeAgent.*: 9793e331f "Honor cluster-provided client config
	// for the node-agent default".
	"client.nodeAgent": semver.MustParse("2.30.0"),
	// quicTunnel.*: 6244b16a1 "Add opt-in QUIC transport for the tunnel".
	"quicTunnel": semver.MustParse("2.31.0"),
	// security.*: 82d025e0f "Admit security.authentication.mode in the
	// chart values schema".
	"security": semver.MustParse("2.31.0"),
	// extraEnv/extraVolumes/extraVolumeMounts: df0c58b98 "Add coverage
	// support and a regression CI job"; first ships in 2.31.2.
	"extraEnv":          semver.MustParse("2.31.2"),
	"extraVolumes":      semver.MustParse("2.31.2"),
	"extraVolumeMounts": semver.MustParse("2.31.2"),
}

// PruneForVersion deletes, from a values map marshaled from Values, every
// key in keyIntroduced newer than target. This is the smallest-viable guard
// against handing an old chart's values.schema.yaml (additionalProperties:
// false at nearly every level) a key it doesn't recognize yet, which fails
// `helm install`/`upgrade` outright instead of ignoring the extra key.
func PruneForVersion(m map[string]any, target semver.Version) {
	for path, introduced := range keyIntroduced {
		if target.LT(introduced) {
			deletePath(m, strings.Split(path, "."))
		}
	}
}

// deletePath removes the nested map key named by path from m, doing
// nothing if any segment along the way is absent or not itself a map.
func deletePath(m map[string]any, path []string) {
	if len(path) == 0 {
		return
	}
	if len(path) == 1 {
		delete(m, path[0])
		return
	}
	sub, ok := m[path[0]].(map[string]any)
	if !ok {
		return
	}
	deletePath(sub, path[1:])
}

// minTokenClient is the first release whose client sends a bearer token with
// every manager call. An older client is refused by a manager that enforces
// authentication.
//
//nolint:gochecknoglobals // static, checked-in compat table
var minTokenClient = semver.MustParse("2.31.0")

// PinPermissiveForClient returns v with security.authentication.mode set to
// permissive when client sends no token (a release older than 2.31.0) and v
// leaves the mode unset. A spec that names a mode keeps it.
func PinPermissiveForClient(v Values, client semver.Version) Values {
	final := semver.Version{Major: client.Major, Minor: client.Minor, Patch: client.Patch}
	if v.Security.Authentication.Mode == "" && final.LT(minTokenClient) {
		v.Security.Authentication.Mode = "permissive"
	}
	return v
}
