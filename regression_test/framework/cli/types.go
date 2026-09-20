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
// object produced by `telepresence intercept|replace|wiretap --format json
// --detailed-output` (pkg/client/cli/intercept/info.go: Info; field names
// verified against that file's json tags). replace and wiretap share this
// same output type with intercept, distinguished by the Replace/Wiretap
// flags.
type InterceptInfo struct {
	ID           string `json:"id,omitempty"`
	Name         string `json:"name,omitempty"`
	Disposition  string `json:"disposition,omitempty"`
	WorkloadKind string `json:"workload_kind,omitempty"`
	// PortID identifies the service port the intercept targets (name or
	// number, pkg/client/cli/intercept/info.go's Info.PortID, spec.PortIdentifier).
	PortID string `json:"port_id,omitempty"`
	// TargetPort/ContainerPort are the intercept's local port and the
	// container port it replaces, respectively.
	TargetPort    int32 `json:"target_port,omitempty"`
	ContainerPort int32 `json:"container_port,omitempty"`
	// PodIP is the IP of the pod serving the attachment, dialable from the
	// test host through the connected session.
	PodIP   string `json:"pod_ip,omitempty"`
	Replace bool   `json:"replace,omitempty"`
	Wiretap bool   `json:"wiretap,omitempty"`
	// Environment carries the intercepted container's environment plus the
	// TELEPRESENCE_ROOT/TELEPRESENCE_INTERCEPT_ID/TELEPRESENCE_API_HOST
	// entries the CLI adds locally before printing (pkg/client/cli/
	// intercept/info.go's Info.Environment, json tag "environment"; see
	// rt.MountRoot's doc comment for how those additions reach here).
	Environment map[string]string `json:"environment,omitempty"`
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
// (pkg/client/cli/cmd/list.go, rpc/connector.WorkloadInfo; field names
// verified against rpc/connector/connector.pb.go's WorkloadInfo json tags:
// intercept_info, ingest_info).
type ListEntry struct {
	Name                   string `json:"name,omitempty"`
	Namespace              string `json:"namespace,omitempty"`
	WorkloadResourceType   string `json:"workload_resource_type,omitempty"`
	AgentVersion           string `json:"agent_version,omitempty"`
	NotInterceptableReason string `json:"not_interceptable_reason,omitempty"`
	// InterceptInfo/IngestInfo are non-empty exactly when the workload
	// currently carries an intercept/replace/wiretap or an ingest,
	// respectively: a suite can assert attach/detach visibility by checking
	// their length, and (via ListEntryIntercept.Spec.Name/ListEntryIngest.
	// Workload) which attachment.
	InterceptInfo []ListEntryIntercept `json:"intercept_info,omitempty"`
	IngestInfo    []ListEntryIngest    `json:"ingest_info,omitempty"`
}

// ListEntryIntercept mirrors the fields the framework asserts on in one
// element of ListEntry.InterceptInfo: rpc/manager.InterceptInfo, whose Name
// is nested under Spec (rpc/manager/manager.pb.go's InterceptInfo/
// InterceptSpec json tags: id, spec.name).
type ListEntryIntercept struct {
	ID   string `json:"id,omitempty"`
	Spec struct {
		Name string `json:"name,omitempty"`
	} `json:"spec"`
}

// ListEntryIngest mirrors the fields the framework asserts on in one element
// of ListEntry.IngestInfo: rpc/connector.IngestInfo (rpc/connector/
// connector.pb.go's json tags: workload, container).
type ListEntryIngest struct {
	Workload  string `json:"workload,omitempty"`
	Container string `json:"container,omitempty"`
}
