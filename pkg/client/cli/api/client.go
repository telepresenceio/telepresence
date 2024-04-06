package api

import (
	"io"
	"net/netip"

	"github.com/blang/semver/v4"
	"github.com/distribution/reference"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
)

type Client interface {
	// Connect returns a connection that corresponds to the given connect request.
	Connect(cr ConnectRequest) (Connection, error)

	// Connection returns an existing connection. An empty name can be used when only one
	// connection exists.
	Connection(name string) (Connection, error)

	// Connections returns a list of existing connections.
	Connections() ([]*daemon.Info, error)

	// Helm will install, upgrade, or uninstall the traffic-manager.
	Helm(hr *helm.Request, cr ConnectRequest) error

	// Version returns the client version
	Version() semver.Version
}

// Connection represents a Telepresence client connection to a namespace in a cluster.
type Connection interface {
	io.Closer

	// Namespace returns the connected namespace
	Namespace() string

	// AgentImage returns the Reference that denotes the image used by the traffic-agent.
	AgentImage() (reference.Reference, error)

	// StartIntercept starts a new intercept. The mountPoint is either a path indicating
	// where to mount the intercepted container's volumes, the string "true" to
	// mount to a generated temporary folder, or empty to disable mounting altogether.
	StartIntercept(rq InterceptRequest, mountPoint string) (*intercept.Info, error)

	// RunIntercept starts a new intercept, executes the given command, then ends the intercept.
	RunIntercept(InterceptRequest, InterceptHandler) (*intercept.Info, error)

	// Info returns the ConnectInfo for the connection.
	Info() *connector.ConnectInfo

	// DaemonInfo returns information about the daemon that manages the current connection.
	DaemonInfo() (*daemon.Info, error)

	// Disconnect tells the daemon to disconnect from the cluster and end the session.
	Disconnect() error

	// List lists the workloads in the given namespace that are possible to intercept. If
	// namespace is an empty string, the current namespace will be used.
	List(namespace string) ([]*connector.WorkloadInfo, error)

	// EndIntercept ends a previously started intercept.
	EndIntercept(name string) error
}

type SubnetViaWorkload struct {
	Subnet   string
	Workload string
}

type ConnectRequest struct {
	// Kubernetes flags to use when connecting. Multi-values must be in CSV form
	KubeFlags map[string]string

	// KubeConfig YAML, if not to be loaded from file.
	KubeConfigData []byte

	// Name of this connection
	Name string

	// MappedNamespaces can be used to limit the namespaces that the DNS will
	// treat as top level domain names.
	MappedNamespaces []string

	// ManagerNamespace is the namespace where the traffic-manager lives. Will
	// default to "ambassador".
	ManagerNamespace string

	// AlsoProxy are subnets that the VIF will route in addition to the subnets
	// that the traffic-manager announces from the cluster.
	AlsoProxy []netip.Prefix

	// NeverProxy are subnets that the VIF will refrain from routing although they
	// were announced by the traffic-manager.
	NeverProxy []netip.Prefix

	// AllowConflictingSubnets are subnets that are allowed to be in conflict with
	// other subnets in the client's network. Telepresence will try to give the VIF
	// higher priority for those subnets.
	AllowConflictingSubnets []netip.Prefix

	// SubnetVieWorkloads are subnet to workload mappings that will cause virtual subnets
	// to be used in the client and the routed to the given workload.
	SubnetViaWorkloads []SubnetViaWorkload

	// If set, then use a containerized daemon for the connection.
	Docker bool

	// Ports exposed by a containerized daemon. Only valid when Docker == true
	ExposedPorts []string

	// Hostname used by a containerized daemon. Only valid when Docker == true
	Hostname string

	// UserDaemonProfilingPort port to use when profiling the user daemon
	UserDaemonProfilingPort uint16

	// RootDaemonProfilingPort port to use when profiling the root daemon
	RootDaemonProfilingPort uint16
}
