package rootd

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd/dns"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

// callerSection says what the root daemon does with one top-level section of the client configuration
// that a caller sends.
type callerSection int

const (
	// ownSection keeps the section of the root daemon's own configuration.
	ownSection callerSection = iota

	// callerSectionAlways takes the section from the caller.
	callerSectionAlways

	// callerSectionUnmanaged takes the section from the caller unless the root daemon is a managed service.
	callerSectionUnmanaged
)

// callerSections holds a decision for every top-level section of the client configuration, keyed by the
// section's JSON name. The sections that the session code never reads, and the ones that name files,
// directories, programs, or images, stay the root daemon's own.
var callerSections = map[string]callerSection{ //nolint:gochecknoglobals // constant
	"cluster":   callerSectionAlways,
	"dns":       callerSectionAlways,
	"docker":    ownSection,
	"grpc":      callerSectionAlways,
	"helm":      ownSection,
	"images":    ownSection,
	"intercept": ownSection,
	"logLevels": callerSectionUnmanaged,
	"network":   callerSectionAlways,
	"nodeAgent": ownSection,
	"routing":   callerSectionAlways,
	"timeouts":  callerSectionAlways,
	"usage":     ownSection,
}

// callerStringFields holds a decision for every field in a section that is taken from the caller and
// that carries text, keyed by "<section>.<field>" using JSON names. Text can name a file, so each
// entry says why the field is safe.
var callerStringFields = map[string]string{ //nolint:gochecknoglobals // constant
	"cluster.defaultManagerNamespace": "a Kubernetes namespace name",
	"cluster.mappedNamespaces":        "Kubernetes namespace names",
	"cluster.managerAddress":          "an address that the session dials, like the server in the kubeconfig",
	"cluster.managerServerCA":         "reduced to inline PEM; see inlineManagerServerCA",
	"cluster.virtualIPSubnet":         "deprecated, and parsed only when the configuration file is loaded",
	"dns.error":                       "a message",
	"dns.includeSuffixes":             "DNS name suffixes",
	"dns.excludeSuffixes":             "DNS name suffixes",
	"dns.excludes":                    "DNS names",
	"dns.mappings":                    "DNS names",
}

// sessionConfig returns the client configuration for a session. It is the root daemon's own configuration
// with the sections that the caller may set taken from the caller's configuration (JSON). No setting that
// names a file, a directory, a program, or an image comes from the caller.
func sessionConfig(ctx context.Context, own client.Config, callerJSON []byte, managed bool) (client.Config, error) {
	cfg := own.Merge(client.GetDefaultConfig())
	if len(bytes.TrimSpace(callerJSON)) == 0 {
		return cfg, nil
	}
	cc, err := client.UnmarshalJSONConfig(callerJSON, false)
	if err != nil {
		return nil, err
	}
	for name, decision := range callerSections {
		if decision == callerSectionAlways || (decision == callerSectionUnmanaged && !managed) {
			copySection(name, cfg, cc)
		}
	}
	d := cfg.DNS()
	d.IncludeSuffixes = dns.ValidNames(ctx, "include suffix", d.IncludeSuffixes, true)
	d.ExcludeSuffixes = dns.ValidNames(ctx, "exclude suffix", d.ExcludeSuffixes, true)
	ca, err := inlineManagerServerCA(cfg.Cluster().ManagerServerCA)
	if err != nil {
		return nil, err
	}
	cfg.Cluster().ManagerServerCA = ca
	return cfg, nil
}

// copySection copies the section with the given JSON name from src to dst. It reports whether
// the name is a section that it knows.
func copySection(name string, dst, src client.Config) bool {
	switch name {
	case "cluster":
		*dst.Cluster() = *src.Cluster()
	case "dns":
		*dst.DNS() = *src.DNS()
	case "grpc":
		*dst.Grpc() = *src.Grpc()
	case "logLevels":
		*dst.LogLevels() = *src.LogLevels()
	case "network":
		*dst.OSSpecific() = *src.OSSpecific()
	case "routing":
		*dst.Routing() = *src.Routing()
	case "timeouts":
		*dst.Timeouts() = *src.Timeouts()
	default:
		return false
	}
	return true
}

// inlineManagerServerCA returns a cluster.managerServerCA setting as PEM text that starts with the
// first PEM header. Besides a PEM block, the setting can be a base64-encoded PEM block, but not the path
// to a file. The result is never taken for a file path when it is used.
func inlineManagerServerCA(spec string) (string, error) {
	if spec == "" {
		return "", nil
	}
	if i := strings.Index(spec, pemPrefix); i >= 0 {
		return spec[i:], nil
	}
	if data, err := base64.StdEncoding.DecodeString(spec); err == nil {
		if i := strings.Index(string(data), pemPrefix); i >= 0 {
			return string(data)[i:], nil
		}
	}
	return "", errcat.Config.New("cluster.managerServerCA must be a PEM block or a base64-encoded PEM block; the root daemon does not read files")
}

const pemPrefix = "-----BEGIN"
