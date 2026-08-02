// Package compose builds docker-compose.yml projects that carry the
// telepresence x-tele extension: a top-level Connections (and Mounts) block
// plus per-service Extension blocks. Field names mirror
// pkg/client/cli/docker/compose/{toplevelextension,extension,attachment,
// connection}.go and docs/reference/compose.md; the extension is plain YAML
// the `telepresence compose` command parses, so the framework never imports
// those product packages.
package compose

// Project is a docker-compose.yml plus its x-tele extension.
type Project struct {
	// Connections becomes the top-level x-tele.connections block. A single
	// implicit connection, using the current kube context's default
	// namespace, is created when this is empty.
	Connections []Connection
	// Mounts becomes the top-level x-tele.mounts block: policies for
	// volumes the service extensions' workloads share with the
	// traffic-agent they attach to.
	Mounts []MountPolicy
	// Services is the compose services block, keyed by service name.
	Services map[string]*Service
	// Volumes declares docker-compose.yml's standard top-level named
	// volumes, keyed by the compose-local volume name; used by suites
	// asserting on `docker volume inspect` survival across `compose down`.
	Volumes map[string]*Volume
}

// Connection is one entry of the top-level x-tele.connections block
// (pkg/client/cli/docker/compose/connection.go's connectionConfig).
type Connection struct {
	// Name identifies the connection; required only when more than one is
	// declared (a service's Extension then names it via its own
	// Connection field).
	Name             string
	Namespace        string
	AlsoProxy        []string
	NeverProxy       []string
	ManagerNamespace string
	MappedNamespaces []string
}

// MountPolicy configures how a service extension's attached workload shares
// a docker-compose volume with the traffic-agent (top-level x-tele.mounts,
// pkg/client/cli/docker/compose/toplevelextension.go's volumeMountPolicy).
type MountPolicy struct {
	// Volume names a single docker-compose volume; mutually exclusive with
	// VolumePattern.
	Volume string
	// VolumePattern is a regular expression matching one or more volumes;
	// mutually exclusive with Volume.
	VolumePattern string
	// Policy is one of "local", "ignore", "remote", or "remoteReadOnly".
	Policy string
}

// Service is one docker-compose.yml service, optionally carrying an x-tele
// Extension.
type Service struct {
	// XTele is the service's x-tele extension block; nil for a plain
	// compose service with no telepresence behavior.
	XTele Extension

	Image   string
	Command []string
	// Ports is docker-compose's own host:container port-publishing list,
	// unrelated to a Proxy/Intercept/Replace extension's own "ports" field.
	Ports       []string
	Environment map[string]string
	Volumes     []string
}

// Volume is a top-level named volume declaration.
type Volume struct {
	// Name overrides the Docker volume name; empty uses compose's
	// generated <project>_<key> name.
	Name string
}
