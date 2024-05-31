package api

import (
	"net/netip"
)

type InterceptRequest struct {
	// Name of the intercept.
	Name string

	// Name of the intercepted workload. Will default to intercept name.
	WorkloadName string

	// Port string. Can contain three fields separated by colon. The interpretation of
	// the fields differ depending on if Docker is true or false.
	//
	//   With Docker == false
	//     <local port number>
	//     <local port number>:<service port identifier>
	//
	//   With Docker == true
	//     <local port number>:<container port number>
	//     <local port number>:<container port number>:<service port identifier>
	Port string

	// ServiceName is the name of the intercepted service. Only needed to resolve ambiguities in
	// case multiple services use the same workload.
	ServiceName string

	// Address The local IP address, in case the intercepted traffic should be sent to something other
	// than localhost.
	Address netip.Addr

	// LocalMountPort is a port where the remote sftp server can be reached. If set, then Telepresence
	// will assume that the caller is responsible for starting the sshfs client that will do the mounting.
	LocalMountPort uint16

	// Replace indicates that the intercepted container should be replaced by the intercept, and then
	// restored when the intercept ends.
	Replace bool

	// EnvFile denotes the path to a file that will receive the intercepted containers environment in a
	// Docker Compose format. See https://docs.docker.com/compose/env-file/ for details.
	EnvFile string

	// EnvJSON denotes the path to a file that will receive the intercepted environment as a JSON object.
	EnvJSON string

	// ToPod adds additional ports to forward from the intercepted pod, will be made available at localhost:PORT.
	// Use this to, for example, access proxy/helper sidecars in the intercepted pod.
	ToPod []netip.AddrPort

	// ToPodUDP is like ToPod, but uses UDP protocol.
	ToPodUDP []netip.AddrPort

	// Silent will silence the intercept information. It will not silence the intercept handler.
	Silent bool
}

type InterceptHandlerType int

const (
	CommandHandler InterceptHandlerType = iota
	DockerRunHandler
	DockerBuildHandler
)

type InterceptHandler interface {
	Type() InterceptHandlerType
}

type CmdHandler struct {
	// MountPoint is the path to where the remote container's mounts will be mounted. A temporary directory
	// will be used if MountPoint is unset.
	//
	// MountPoint is either a path indicating where to mount the intercepted container's volumes, the string
	// "true", to mount to a generated temporary folder, or empty to disable mounting altogether.
	MountPoint string

	// CmdLine a command to execute during the time when the intercept is active.
	Cmdline []string
}

func (CmdHandler) Type() InterceptHandlerType {
	return CommandHandler
}

type DockerCommon struct {
	// Mount if true, will cause the volumes of the remote container to be mounted using
	// the telemount Docker volume plugin.
	Mount bool

	// Options for the docker run command. Must be in the form <key>=<value> or just <key>
	// for boolean options. Short form options are not supported so `-it` must be added as
	// []string{"interactive", "tty"}
	Options []string

	// Arguments for to pass to the container
	Arguments []string

	// Debug uses relaxed security to allow a debugger run in the container.
	// Mutually exclusive to DockerRun and DockerBuild.
	Debug bool
}

type DockerRunInterceptHandler struct {
	DockerCommon

	// Image is the image tag
	Image string
}

func (DockerRunInterceptHandler) Type() InterceptHandlerType {
	return DockerRunHandler
}

type DockerBuildInterceptHandler struct {
	DockerCommon

	// Context docker context, in the form of a path or a URL.
	Context string

	// Options for the docker build command. Must be in the form <key>=<value> or just <key>
	// for boolean options. Short form options are not supported.
	BuildOptions []string
}

func (DockerBuildInterceptHandler) Type() InterceptHandlerType {
	return DockerBuildHandler
}
