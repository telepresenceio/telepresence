package compose

import (
	"context"
	"fmt"
	"strings"

	compose "github.com/compose-spec/compose-go/v2/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/json"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type serviceExtension interface {
	// Activate the service extension.
	activate(*transformer) (*attachment, error)

	deactivate() error

	attached() (*attachment, error)

	attachmentType() types.AttachmentType

	// name used for the proxy or the attachment. It defaults to the name of the compose-service.
	name() string

	// connection used when the extension is activated.
	connection() *connection

	// ConnectionName to use for this extension. It is optional unless more than one connection is present
	// in the top level x-tele extension.
	connectionName() string

	// ComposeService is the extended docker-compose service.
	composeService() *compose.ServiceConfig

	// NeedsVolumes returns true if the extended service has volumes unless this is a "connect" or "proxy" extension.
	needsVolumes() bool

	// SetConnection assigns the connection used when the extension is activated.
	setConnection(c *connection)

	init(*config, types.AttachmentType, *compose.ServiceConfig)
}

type mountsExtension interface {
	serviceExtension

	desiredRemoteMounts(remoteMounts types.MountPolicies) (types.MountPolicies, map[string]*compose.ServiceVolumeConfig)
}

type workloadExtension interface {
	mountsExtension

	// workload is the name of the attached workload. It defaults to name().
	workload() string

	createInterceptRequest(localMountPort uint16) (*connector.CreateInterceptRequest, error)
}

type servicePortExtension interface {
	serviceExtension
	servicePorts() []types.PortMapping
}

func (c *config) parseServiceExtension(composeService *compose.ServiceConfig, v any) (se serviceExtension, err error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errcat.User.Newf("%s extension is not a map", extensionKey)
	}
	typ, ok := m["type"].(string)
	if !ok {
		return nil, errcat.User.Newf("%s extension must have a type", extensionKey)
	}
	et, err := types.ParseAttachmentType(typ)
	if err != nil {
		return nil, errcat.User.Newf("%s extension has invalid type: %v", extensionKey, err)
	}

	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	switch et {
	case types.AttachmentTypeConnect:
		se = &extension{}
	case types.AttachmentTypeIngest:
		se = &ingestExtension{}
	case types.AttachmentTypeIntercept:
		se = &interceptExtension{}
	case types.AttachmentTypeProxy:
		se = &proxyExtension{}
	case types.AttachmentTypeReplace:
		se = &replaceExtension{}
	case types.AttachmentTypeWiretap:
		se = &wiretapExtension{}
	default:
		return nil, errcat.User.Newf("%s has unsupported extension type %s", extensionKey, et)
	}

	err = json.Unmarshal(data, se, true)
	if err != nil {
		return nil, errcat.User.New(err)
	}
	se.init(c, et, composeService)
	return se, nil
}

type extension struct {
	Type       types.AttachmentType `json:"type"`
	Connection string               `json:"connection,omitempty"`
	composeSvc *compose.ServiceConfig
	conn       *connection
}

func (e *extension) init(_ *config, et types.AttachmentType, composeService *compose.ServiceConfig) {
	e.Type = et
	e.composeSvc = composeService
}

// Name is the name of the service that this proxy connects to. It defaults to the name of the compose-service.
func (e *extension) name() string {
	return e.composeSvc.Name
}

func (e *extension) attachmentType() types.AttachmentType {
	return e.Type
}

func (e *extension) connectionName() string {
	return e.Connection
}

// ComposeService is the name of the extended docker-compose service.
func (e *extension) composeService() *compose.ServiceConfig {
	return e.composeSvc
}

func (e *extension) connection() *connection {
	return e.conn
}

func (e *extension) attached() (*attachment, error) {
	return createAttachment(daemon.MustGetUserClient(e.conn), e, 0)
}

func (e *extension) needsVolumes() bool {
	return false
}

func (e *extension) setConnection(c *connection) {
	e.conn = c
}

func (e *extension) activate(*transformer) (*attachment, error) {
	return createAttachment(daemon.MustGetUserClient(e.conn), e, 0)
}

func (e *extension) deactivate() error {
	return nil
}

type proxyExtension struct {
	extension
	Name  string              `json:"name"`
	Ports []types.PortMapping `json:"ports,omitempty"`
}

func (e *proxyExtension) init(c *config, et types.AttachmentType, composeService *compose.ServiceConfig) {
	e.extension.init(c, et, composeService)
	if e.Name == "" {
		e.Name = composeService.Name
	}
}

func (e *proxyExtension) activate(*transformer) (*attachment, error) {
	return createAttachment(daemon.MustGetUserClient(e.conn), e, 0)
}

// Name is the name of the service that this proxy connects to. It defaults to the name of the compose-service.
func (e *proxyExtension) name() string {
	return e.Name
}

// ServicePorts mappings from local ports to service ports.
func (e *proxyExtension) servicePorts() []types.PortMapping {
	return e.Ports
}

type attachExtension struct {
	extension
	Name   string `json:"name"`
	mounts []volumeMountPolicy
}

func (e *attachExtension) init(c *config, et types.AttachmentType, composeService *compose.ServiceConfig) {
	e.extension.init(c, et, composeService)
	if e.Name == "" {
		e.Name = composeService.Name
	}
	e.mounts = c.Mounts
}

// Name is the name of the attachment. It defaults to the name of the compose-service.
func (e *attachExtension) name() string {
	return e.Name
}

// Workload is the name of the attached workload. It defaults to Name.
func (e *attachExtension) workload() string {
	return e.Name
}

func (e *attachExtension) needsVolumes() bool {
	return len(e.composeService().Volumes) > 0
}

func (e *attachExtension) desiredRemoteMounts(remoteMounts types.MountPolicies) (types.MountPolicies, map[string]*compose.ServiceVolumeConfig) {
	desiredMounts := make(types.MountPolicies, len(remoteMounts))
	volumes := make(map[string]*compose.ServiceVolumeConfig)
	composeVolumes := e.composeService().Volumes
	for p, m := range remoteMounts {
		if m == types.MountPolicyRemote || m == types.MountPolicyRemoteReadOnly {
			for vi := range composeVolumes {
				v := &composeVolumes[vi]
				if v.Type == compose.VolumeTypeVolume && v.Target == p {
					// This docker compose volume targets the remote mount point.
					for _, mp := range e.mounts {
						if mp.Matches(v.Source) {
							m = mp.Policy
							break
						}
					}
					if m == types.MountPolicyRemote || m == types.MountPolicyRemoteReadOnly {
						v.ReadOnly = v.ReadOnly || m == types.MountPolicyRemoteReadOnly
						volumes[p] = v
						desiredMounts[p] = m
					}
					break
				}
			}
		}
	}
	return desiredMounts, volumes
}

type httpFilterExtension struct {
	attachExtension
	HttpFilters  map[string]string   `json:"httpFilters,omitempty"`
	Metadata     map[string]string   `json:"metadata,omitempty"`
	Ports        []types.PortMapping `json:"ports,omitempty"`
	Paths        []string            `json:"httpPaths,omitempty"`
	PathPrefixes []string            `json:"httpPathPrefixes,omitempty"`
	PathRegexps  []string            `json:"httpPathRegexps,omitempty"`
}

func (e *httpFilterExtension) amendInterceptSpec(spec *manager.InterceptSpec) error {
	ports := e.servicePorts()
	if len(ports) == 0 {
		return fmt.Errorf("a %s requires at least one port", e.attachmentType())
	}
	err := addPortsSpec(spec, ports)
	if err != nil {
		return err
	}
	spec.HeaderFilters = e.HttpFilters
	spec.PathFilters = intercept.BuildPathFilters(e.Paths, e.PathPrefixes, e.PathRegexps)
	if len(spec.HeaderFilters) > 0 || len(spec.PathFilters) > 0 {
		spec.Mechanism = "http"
	}
	spec.Metadata = e.Metadata
	return nil
}

func (e *httpFilterExtension) servicePorts() []types.PortMapping {
	return e.Ports
}

type interceptExtension struct {
	httpFilterExtension
	Workload string               `json:"workload,omitempty"`
	Service  string               `json:"service,omitempty"`
	ToPod    []types.PortAndProto `json:"toPod,omitempty"`
}

func (e *interceptExtension) deactivate() error {
	return deactivateIntercept(e)
}

func (e *interceptExtension) init(c *config, et types.AttachmentType, composeService *compose.ServiceConfig) {
	e.attachExtension.init(c, et, composeService)
	if e.Workload == "" {
		e.Workload = e.Name
	}
}

// Workload is the name of the attached workload. It defaults to Name.
func (e *interceptExtension) workload() string {
	return e.Workload
}

func (e *interceptExtension) activate(t *transformer) (*attachment, error) {
	return activateIntercept(e, t)
}

func (e *interceptExtension) attached() (*attachment, error) {
	return createAttachment(daemon.MustGetUserClient(e.conn), e, 0)
}

func (e *interceptExtension) service() string {
	return e.Service
}

func (e *interceptExtension) toPod() []types.PortAndProto {
	return e.ToPod
}

func (e *interceptExtension) createInterceptRequest(localMountPort uint16) (*connector.CreateInterceptRequest, error) {
	ir := createInterceptRequest(e, localMountPort)
	spec := ir.Spec
	spec.ServiceName = e.service()
	for _, toPod := range e.toPod() {
		spec.LocalPorts = append(spec.LocalPorts, toPod.String())
	}
	err := e.amendInterceptSpec(spec)
	if err != nil {
		return nil, err
	}
	return ir, nil
}

type ingestExtension struct {
	attachExtension
	Container string               `json:"container,omitempty"`
	ToPod     []types.PortAndProto `json:"toPod,omitempty"`
}

func (e *ingestExtension) activate(t *transformer) (*attachment, error) {
	ctx := e.conn
	ud := daemon.MustGetUserClient(ctx)
	sftpPort, err := t.config.getMountPort(e)
	if err != nil {
		return nil, err
	}

	// The ingest might be active already.
	ii, err := ud.GetIngest(ctx, &connector.IngestIdentifier{WorkloadName: e.workload()})
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, grpc.FromGRPC(err)
		}
	}
	if ii == nil {
		ir := &connector.IngestRequest{
			Identifier: &connector.IngestIdentifier{
				WorkloadName:  e.name(),
				ContainerName: e.container(),
			},
			LocalMountPort: int32(sftpPort),
		}
		for _, toPod := range e.toPod() {
			ir.LocalPorts = append(ir.LocalPorts, toPod.String())
		}
		ii, err = ud.Ingest(e.conn, ir)
		if err != nil {
			return nil, grpc.FromGRPC(err)
		}
	}
	at, err := createAttachment(ud, e, sftpPort)
	if err != nil {
		return nil, err
	}
	at.assignEnvAndCreateMounts(ii.Environment, ii.Mounts, t)
	return at, nil
}

func (e *ingestExtension) container() string {
	return e.Container
}

func (e *ingestExtension) deactivate() error {
	ctx := context.WithoutCancel(e.connection().Context)
	ud := daemon.MustGetUserClient(ctx)
	ig, err := ud.GetIngest(ctx, &connector.IngestIdentifier{
		WorkloadName:  e.workload(),
		ContainerName: e.container(),
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			err = nil
		}
		return grpc.FromGRPC(err)
	}
	_, err = ud.LeaveIngest(ctx, &connector.IngestIdentifier{
		WorkloadName:  ig.Workload,
		ContainerName: ig.Container,
		Namespace:     ig.Namespace,
	})
	return grpc.FromGRPC(err)
}

func (e *ingestExtension) attached() (*attachment, error) {
	return createAttachment(daemon.MustGetUserClient(e.conn), e, 0)
}

// ToPod maps local ports to ports in an attached pod.
func (e *ingestExtension) toPod() []types.PortAndProto {
	return e.ToPod
}

type replaceExtension struct {
	attachExtension

	// Container is the name of the container that Telepresence will replace.
	Container string `json:"container,omitempty"`

	// Ports maps container ports to local ports.
	Ports []types.PortMapping `json:"ports,omitempty"`

	// ToPod maps local ports to ports in an attached pod.
	ToPod []types.PortAndProto `json:"toPod,omitempty"`
}

func (e *replaceExtension) activate(t *transformer) (*attachment, error) {
	return activateIntercept(e, t)
}

func (e *replaceExtension) deactivate() error {
	return deactivateIntercept(e)
}

func (e *replaceExtension) attached() (*attachment, error) {
	return createAttachment(daemon.MustGetUserClient(e.conn), e, 0)
}

func (e *replaceExtension) container() string {
	return e.Container
}

func (e *replaceExtension) containerPorts() []types.PortMapping {
	return e.Ports
}

func (e *replaceExtension) toPod() []types.PortAndProto {
	return e.ToPod
}

type wiretapExtension struct {
	httpFilterExtension

	// Service is the Kubernetes service that Telepresence will attach to.
	Service string `json:"service,omitempty"`
}

func (e *wiretapExtension) activate(t *transformer) (*attachment, error) {
	return activateIntercept(e, t)
}

func (e *wiretapExtension) deactivate() error {
	return deactivateIntercept(e)
}

func (e *wiretapExtension) attached() (*attachment, error) {
	return createAttachment(daemon.MustGetUserClient(e.conn), e, 0)
}

func (e *wiretapExtension) service() string {
	return e.Service
}

func (e *wiretapExtension) createInterceptRequest(localMountPort uint16) (*connector.CreateInterceptRequest, error) {
	ir := createInterceptRequest(e, localMountPort)
	spec := ir.Spec
	spec.ServiceName = e.service()
	spec.Wiretap = true
	ir.MountReadOnly = true
	err := e.amendInterceptSpec(spec)
	if err != nil {
		return nil, err
	}
	return ir, nil
}

func (e *replaceExtension) createInterceptRequest(localMountPort uint16) (*connector.CreateInterceptRequest, error) {
	ir := createInterceptRequest(e, localMountPort)
	spec := ir.Spec
	spec.ContainerName = e.container()
	spec.Replace = true
	spec.NoDefaultPort = true
	for _, toPod := range e.toPod() {
		spec.LocalPorts = append(spec.LocalPorts, toPod.String())
	}
	ports := e.containerPorts()
	if len(ports) == 0 {
		spec.PortIdentifier = "all"
		return ir, nil
	}
	err := addPortsSpec(spec, ports)
	if err != nil {
		return nil, err
	}
	return ir, nil
}

func addPortsSpec(spec *manager.InterceptSpec, ports []types.PortMapping) error {
	p0 := ports[0]
	spec.PortIdentifier = p0.To().String()
	spec.TargetPort = int32(p0.FromAsNumeric().Port)
	for i := 1; i < len(ports); i++ {
		pm := ports[i].String()
		if colIdx := strings.IndexByte(pm, ':'); colIdx > 0 {
			// An entry in the "ports" list puts the local port first, but it's the destination in the pod-port mapping.
			to := pm[:colIdx]
			from := pm[colIdx+1:]
			if slashIdx := strings.IndexByte(from, '/'); slashIdx > 0 {
				from = from[:slashIdx]
				to += from[slashIdx:]
			}
			pm = from + ":" + to
		}
		if err := types.PortMapping(pm).Validate(); err != nil {
			return err
		}
		spec.PodPorts = append(spec.PodPorts, pm)
	}
	return nil
}

func createInterceptRequest(e workloadExtension, localMountPort uint16) *connector.CreateInterceptRequest {
	spec := &manager.InterceptSpec{
		Name:       e.name(),
		Mechanism:  "tcp",
		Agent:      e.workload(),
		TargetHost: e.composeService().Name,
	}
	ir := &connector.CreateInterceptRequest{
		Spec:           spec,
		LocalMountPort: int32(localMountPort),
	}
	return ir
}

func activateIntercept(e workloadExtension, t *transformer) (*attachment, error) {
	ctx := e.connection()
	ud := daemon.MustGetUserClient(ctx)
	sftpPort, err := t.config.getMountPort(e)
	if err != nil {
		return nil, err
	}

	// The intercept might be active already.
	ii, err := ud.GetIntercept(ctx, &manager.GetInterceptRequest{Name: e.name()})
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, grpc.FromGRPC(err)
		}
	}
	if ii == nil {
		ir, err := e.createInterceptRequest(sftpPort)
		if err != nil {
			return nil, err
		}
		ii, err = ud.CreateIntercept(e.connection(), ir)
		if err = grpc.FromGRPC(err); err != nil {
			return nil, fmt.Errorf("connector.CreateIntercept: %w", err)
		}
	}
	at, err := createAttachment(ud, e, sftpPort)
	if err != nil {
		return nil, err
	}
	at.assignEnvAndCreateMounts(ii.Environment, ii.Mounts, t)
	return at, nil
}

func deactivateIntercept(e workloadExtension) error {
	ctx := context.WithoutCancel(e.connection().Context)
	ud := daemon.MustGetUserClient(ctx)
	ic, err := ud.GetIntercept(ctx, &manager.GetInterceptRequest{Name: e.name()})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			err = nil
		}
		return grpc.FromGRPC(err)
	}
	_, err = ud.RemoveIntercept(ctx, &manager.RemoveInterceptRequest2{Name: ic.Spec.Name})
	return grpc.FromGRPC(err)
}
