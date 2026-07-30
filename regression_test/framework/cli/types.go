package cli

// Status mirrors the fields the framework asserts on in the JSON object
// produced by `telepresence status --output json`
// (pkg/client/cli/cmd/status.go: StatusInfo/RootDaemonStatus/UserDaemonStatus/
// TrafficManagerStatus).
type Status struct {
	UserDaemon     UserDaemonStatus     `json:"user_daemon"`
	RootDaemon     RootDaemonStatus     `json:"root_daemon"`
	TrafficManager TrafficManagerStatus `json:"traffic_manager"`
}

// UserDaemonStatus is the subset of pkg/client/cli/cmd/status.go's
// UserDaemonStatus the framework asserts on.
type UserDaemonStatus struct {
	Running          bool   `json:"running,omitempty"`
	Version          string `json:"version,omitempty"`
	Namespace        string `json:"namespace,omitempty"`
	ManagerNamespace string `json:"manager_namespace,omitempty"`
}

// RootDaemonStatus is the subset of pkg/client/cli/cmd/status.go's
// RootDaemonStatus the framework asserts on.
type RootDaemonStatus struct {
	Running bool   `json:"running,omitempty"`
	Version string `json:"version,omitempty"`
}

// TrafficManagerStatus is the subset of pkg/client/cli/cmd/status.go's
// TrafficManagerStatus the framework asserts on.
type TrafficManagerStatus struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// Version mirrors the fields the framework asserts on in the JSON object
// produced by `telepresence version --output json`
// (pkg/client/cli/cmd/version.go: versionInfo).
type Version struct {
	Client         string `json:"client,omitempty"`
	RootDaemon     string `json:"root_daemon,omitempty"`
	UserDaemon     string `json:"user_daemon,omitempty"`
	TrafficManager string `json:"traffic_manager,omitempty"`
}

// InterceptInfo mirrors the fields the framework asserts on in the JSON
// object produced by `telepresence intercept --format json`
// (pkg/client/cli/intercept/info.go: Info).
type InterceptInfo struct {
	ID           string `json:"id,omitempty"`
	Name         string `json:"name,omitempty"`
	Disposition  string `json:"disposition,omitempty"`
	WorkloadKind string `json:"workload_kind,omitempty"`
}

// IngestInfo mirrors the fields the framework asserts on in the JSON object
// produced by `telepresence ingest --format json`
// (pkg/client/cli/ingest/info.go: Info).
type IngestInfo struct {
	WorkloadName string            `json:"workload_name,omitempty"`
	WorkloadKind string            `json:"workload_kind,omitempty"`
	Container    string            `json:"container,omitempty"`
	Environment  map[string]string `json:"environment,omitempty"`
}

// ListEntry mirrors the fields the framework asserts on in one element of the
// JSON array produced by `telepresence list --format json`
// (pkg/client/cli/cmd/list.go, rpc/connector.WorkloadInfo).
type ListEntry struct {
	Name                   string `json:"name,omitempty"`
	Namespace              string `json:"namespace,omitempty"`
	WorkloadResourceType   string `json:"workload_resource_type,omitempty"`
	AgentVersion           string `json:"agent_version,omitempty"`
	NotInterceptableReason string `json:"not_interceptable_reason,omitempty"`
}
