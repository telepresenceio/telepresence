// Package state builds `telepresence apply`/`delete` WorkstationState
// manifests: typed structs producing YAML validated by the schema
// pkg/client/cli/manifest/state.schema.yaml enforces (apiVersion:
// telepresence.io/v1alpha1, kind: WorkstationState, an optional connection,
// and an ordered attachments list). Field names mirror
// pkg/client/cli/manifest/types.go; apiVersion/kind are fixed by the schema,
// so State doesn't expose them.
package state

// State is a WorkstationState manifest.
type State struct {
	// Connection, when set, becomes the manifest's connection block: the
	// area's apply/delete invocations then establish/tear down their own
	// session, so the manifest's connection is named (regression_test/
	// README.md's Connections rule) rather than sharing the default one.
	Connection *Connection
	// Attachments are established (in order) by apply and removed (in
	// reverse order) by delete.
	Attachments []Attachment
}

// Connection mirrors pkg/client/cli/manifest/types.go's Connection: the
// flags of `telepresence connect`.
type Connection struct {
	// Name defaults to a name derived from the kube context and namespace.
	Name                    string
	Kubeconfig              string
	Context                 string
	Namespace               string
	KubeFlags               map[string]string
	ManagerNamespace        string
	MappedNamespaces        []string
	AlsoProxy               []string
	NeverProxy              []string
	AllowConflictingSubnets []string
	// Vnat is shorthand for a ProxyVia entry with Workload "local".
	Vnat     []string
	ProxyVia []ProxyVia
	// RerouteLocal format: <local port>:<host>:<port>[/{tcp,udp}].
	RerouteLocal []string
	// RerouteRemote format: <host>:<port>:<new port>[/{tcp,udp}].
	RerouteRemote []string
}

// ProxyVia mirrors pkg/client/cli/manifest/types.go's ProxyVia: a subnet
// (CIDR, or "service"/"pods"/"also"/"all") translated via a workload
// ("local" is what a Connection.Vnat entry expands to).
type ProxyVia struct {
	Subnet   string
	Workload string
}

// AttachmentType selects an Attachment's kind.
type AttachmentType string

const (
	TypeIntercept AttachmentType = "intercept"
	TypeReplace   AttachmentType = "replace"
	TypeIngest    AttachmentType = "ingest"
	TypeWiretap   AttachmentType = "wiretap"
)

// Attachment mirrors pkg/client/cli/manifest/types.go's Attachment: every
// attachment-type-specific field lives on this one flattened struct, same as
// the product's own type, with omitempty pruning what a given Type doesn't
// use. The schema's oneOf constrains which fields are valid per Type (see
// each field's comment); use the Intercept/Replace/Ingest/Wiretap
// constructors to start from a correctly-typed value.
type Attachment struct {
	Type      AttachmentType
	Name      string
	Namespace string
	// Container: defaults to the container matching the first port.
	Container string
	Env       *Env
	Mount     *Mount
	// NodeAgent: use a node-scoped traffic agent.
	NodeAgent *bool
	// Command starts a local handler process after the attachment is
	// established (like the imperative commands' trailing "-- <cmd>
	// <args...>"); restarted when it exits or Command changes, stopped when
	// the attachment is removed.
	Command []string

	// Workload, Service: intercept/wiretap only; Workload defaults to Name.
	Workload string
	Service  string
	// Ports: intercept ("<local port>:<identifier>") and replace
	// ("<local port>:<container port>", or "all"); not valid for
	// ingest/wiretap.
	Ports []string
	// Address: intercept/replace only; defaults to 127.0.0.1.
	Address string
	// Mechanism: intercept/wiretap only; defaults to "tcp".
	Mechanism string

	// HTTPHeaders/HTTPPathEqualities/HTTPPathPrefixes/HTTPPathRegexps/
	// Plaintext: intercept/wiretap only.
	HTTPHeaders        []string
	HTTPPathEqualities []string
	HTTPPathPrefixes   []string
	HTTPPathRegexps    []string
	Plaintext          bool

	// Metadata: intercept only.
	Metadata map[string]string
	// ToPod: intercept/replace/ingest only, not wiretap.
	ToPod []string
}

// Env mirrors pkg/client/cli/manifest/types.go's Env: emission of the
// remote environment to a file.
type Env struct {
	File   string
	Syntax string
	JSON   string
}

// Mount mirrors pkg/client/cli/manifest/types.go's Mount: mounting of the
// remote container's volumes.
type Mount struct {
	// Enabled defaults to true; nil means unspecified.
	Enabled  *bool
	Path     string
	ReadOnly bool
	// LocalMountPort exposes the remote file system over SFTP on this local
	// port instead of mounting it.
	LocalMountPort uint16
}

// Bool returns a pointer to b, for the *bool fields (Attachment.NodeAgent,
// Mount.Enabled) whose zero value (nil) means "unspecified" rather than
// "false".
func Bool(b bool) *bool { return &b }

// Intercept returns an intercept attachment named name (Workload defaults
// to Name product-side).
func Intercept(name string) Attachment {
	return Attachment{Type: TypeIntercept, Name: name}
}

// Replace returns a replace attachment naming the workload whose container
// is replaced; use "<workload>/<container>" in name when Container isn't
// set separately.
func Replace(name string) Attachment {
	return Attachment{Type: TypeReplace, Name: name}
}

// Ingest returns an ingest attachment naming the workload to ingest.
func Ingest(name string) Attachment {
	return Attachment{Type: TypeIngest, Name: name}
}

// Wiretap returns a wiretap attachment named name (Workload defaults to
// Name product-side).
func Wiretap(name string) Attachment {
	return Attachment{Type: TypeWiretap, Name: name}
}
