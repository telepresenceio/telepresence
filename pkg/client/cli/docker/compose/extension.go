package compose

import (
	"context"
	"errors"
	"fmt"
	"strings"

	compose "github.com/compose-spec/compose-go/v2/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type serviceExtension interface {
	// Activate the service extension.
	activate(*transformer) (*engagement, error)

	deactivate() error

	engaged() (*engagement, error)

	engagementType() types.EngagementType

	// name used for the proxy or the engagement. It defaults to the name of the compose-service.
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

	init(*config, types.EngagementType, *compose.ServiceConfig)
}

type mountsExtension interface {
	serviceExtension

	desiredRemoteMounts(remoteMounts types.MountPolicies) (types.MountPolicies, map[string]*compose.ServiceVolumeConfig)
}

type workloadExtension interface {
	mountsExtension

	// workload is the name of the engaged workload. It defaults to name().
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
	et, err := types.ParseEngagementType(typ)
	if err != nil {
		return nil, errcat.User.Newf("%s extension has invalid type: %v", extensionKey, err)
	}

	data, err := client.MarshalJSON(v)
	if err != nil {
		return nil, err
	}
	switch et {
	case types.EngagementTypeConnect:
		se = &extension{}
	case types.EngagementTypeIngest:
		se = &ingestExtension{}
	case types.EngagementTypeIntercept:
		se = &interceptExtension{}
	case types.EngagementTypeProxy:
		se = &proxyExtension{}
	case types.EngagementTypeReplace:
		se = &replaceExtension{}
	case types.EngagementTypeWiretap:
		se = &wiretapExtension{}
	default:
		return nil, errcat.User.Newf("%s has unsupported extension type %s", extensionKey, et)
	}

	err = client.UnmarshalJSON(data, se, true)
	if err != nil {
		return nil, errcat.User.New(err)
	}
	se.init(c, et, composeService)
	return se, nil
}

type extension struct {
	Type       types.EngagementType `json:"type"`
	Connection string               `json:"connection,omitempty"`
	composeSvc *compose.ServiceConfig
	conn       *connection
}

func (e *extension) init(_ *config, et types.EngagementType, composeService *compose.ServiceConfig) {
	e.Type = et
	e.composeSvc = composeService
}

// Name is the name of the service that this proxy connects to. It defaults to the name of the compose-service.
func (e *extension) name() string {
	return e.composeSvc.Name
}

func (e *extension) engagementType() types.EngagementType {
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

func (e *extension) needsVolumes() bool {
	return false
}

func (e *extension) setConnection(c *connection) {
	e.conn = c
}

func (e *extension) activate(*transformer) (*engagement, error) {
	return createEngagement(daemon.MustGetUserClient(e.conn), e, 0)
}

func (e *extension) deactivate() error {
	return nil
}

type proxyExtension struct {
	extension
	Name  string              `json:"name"`
	Ports []types.PortMapping `json:"ports,omitempty"`
}

func (e *proxyExtension) init(c *config, et types.EngagementType, composeService *compose.ServiceConfig) {
	e.extension.init(c, et, composeService)
	if e.Name == "" {
		e.Name = composeService.Name
	}
}

func (e *proxyExtension) activate(*transformer) (*engagement, error) {
	return createEngagement(daemon.MustGetUserClient(e.conn), e, 0)
}

func (e *extension) engaged() (*engagement, error) {
	return createEngagement(daemon.MustGetUserClient(e.conn), e, 0)
}

// Name is the name of the service that this proxy connects to. It defaults to the name of the compose-service.
func (e *proxyExtension) name() string {
	return e.Name
}

// ServicePorts mappings from local ports to service ports.
func (e *proxyExtension) servicePorts() []types.PortMapping {
	return e.Ports
}

type engageExtension struct {
	extension
	Name   string `json:"name"`
	mounts []volumeMountPolicy
}

func (e *engageExtension) init(c *config, et types.EngagementType, composeService *compose.ServiceConfig) {
	e.extension.init(c, et, composeService)
	if e.Name == "" {
		e.Name = composeService.Name
	}
	e.mounts = c.Mounts
}

// Name is the name of the engagement. It defaults to the name of the compose-service.
func (e *engageExtension) name() string {
	return e.Name
}

// Workload is the name of the engaged workload. It defaults to Name.
func (e *engageExtension) workload() string {
	return e.Name
}

func (e *engageExtension) needsVolumes() bool {
	return len(e.composeService().Volumes) > 0
}

func (e *engageExtension) desiredRemoteMounts(remoteMounts types.MountPolicies) (types.MountPolicies, map[string]*compose.ServiceVolumeConfig) {
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

type interceptExtension struct {
	engageExtension
	Workload string               `json:"workload,omitempty"`
	Service  string               `json:"service,omitempty"`
	Ports    []types.PortMapping  `json:"ports,omitempty"`
	ToPod    []types.PortAndProto `json:"toPod,omitempty"`
}

func (e *interceptExtension) deactivate() error {
	return deactivateIntercept(e)
}

func (e *interceptExtension) init(c *config, et types.EngagementType, composeService *compose.ServiceConfig) {
	e.engageExtension.init(c, et, composeService)
	if e.Workload == "" {
		e.Workload = e.Name
	}
}

// Workload is the name of the engaged workload. It defaults to Name.
func (e *interceptExtension) workload() string {
	return e.Workload
}

func (e *interceptExtension) activate(t *transformer) (*engagement, error) {
	return activateIntercept(e, t)
}

func (e *interceptExtension) engaged() (*engagement, error) {
	return createEngagement(daemon.MustGetUserClient(e.conn), e, 0)
}

func (e *interceptExtension) service() string {
	return e.Service
}

func (e *interceptExtension) servicePorts() []types.PortMapping {
	return e.Ports
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
	ports := e.servicePorts()
	if len(ports) == 0 {
		return nil, fmt.Errorf("a %s requires at least one port", e.engagementType())
	}
	err := addPortsSpec(spec, ports)
	if err != nil {
		return nil, err
	}
	return ir, nil
}

type ingestExtension struct {
	engageExtension
	Container string               `json:"container,omitempty"`
	ToPod     []types.PortAndProto `json:"toPod,omitempty"`
}

func (e *ingestExtension) activate(t *transformer) (*engagement, error) {
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
			return nil, err
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
			switch status.Code(err) {
			case codes.AlreadyExists, codes.NotFound, codes.Unimplemented, codes.FailedPrecondition:
				return nil, errors.New(status.Convert(err).Message())
			}
			return nil, fmt.Errorf("ingest: %w", err)
		}
	}
	ae, err := createEngagement(ud, e, sftpPort)
	if err != nil {
		return nil, err
	}
	ae.assignEnvAndCreateMounts(ii.Environment, ii.Mounts, t)
	return ae, nil
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
		return err
	}
	_, err = ud.LeaveIngest(ctx, &connector.IngestIdentifier{
		WorkloadName:  ig.Workload,
		ContainerName: ig.Container,
	})
	return err
}

func (e *ingestExtension) engaged() (*engagement, error) {
	return createEngagement(daemon.MustGetUserClient(e.conn), e, 0)
}

// ToPod maps local ports to ports in an engaged pod.
func (e *ingestExtension) toPod() []types.PortAndProto {
	return e.ToPod
}

type replaceExtension struct {
	engageExtension

	// Container is the name of the container that Telepresence will replace.
	Container string `json:"container,omitempty"`

	// Ports maps container ports to local ports.
	Ports []types.PortMapping `json:"ports,omitempty"`

	// ToPod maps local ports to ports in an engaged pod.
	ToPod []types.PortAndProto `json:"toPod,omitempty"`
}

func (e *replaceExtension) activate(t *transformer) (*engagement, error) {
	return activateIntercept(e, t)
}

func (e *replaceExtension) deactivate() error {
	return deactivateIntercept(e)
}

func (e *replaceExtension) engaged() (*engagement, error) {
	return createEngagement(daemon.MustGetUserClient(e.conn), e, 0)
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
	engageExtension

	// Service is the Kubernetes service that Telepresence will engage with.
	Service string `json:"service,omitempty"`

	// Ports maps service ports to local ports.
	Ports []types.PortMapping `json:"ports,omitempty"`
}

func (e *wiretapExtension) activate(t *transformer) (*engagement, error) {
	return activateIntercept(e, t)
}

func (e *wiretapExtension) deactivate() error {
	return deactivateIntercept(e)
}

func (e *wiretapExtension) engaged() (*engagement, error) {
	return createEngagement(daemon.MustGetUserClient(e.conn), e, 0)
}

func (e *wiretapExtension) service() string {
	return e.Service
}

// ServicePorts to intercept mapped to local ports.
func (e *wiretapExtension) servicePorts() []types.PortMapping {
	return e.Ports
}

func (e *wiretapExtension) createInterceptRequest(localMountPort uint16) (*connector.CreateInterceptRequest, error) {
	ir := createInterceptRequest(e, localMountPort)
	spec := ir.Spec
	spec.ServiceName = e.service()
	spec.Wiretap = true
	ir.MountReadOnly = true

	ports := e.servicePorts()
	if len(ports) == 0 {
		return nil, fmt.Errorf("a %s requires at least one port", e.engagementType())
	}
	err := addPortsSpec(spec, ports)
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

func activateIntercept(e workloadExtension, t *transformer) (*engagement, error) {
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
			return nil, err
		}
	}
	if ii == nil {
		ir, err := e.createInterceptRequest(sftpPort)
		if err != nil {
			return nil, err
		}
		r, err := ud.CreateIntercept(e.connection(), ir)
		if err = intercept.Result(r, err); err != nil {
			return nil, fmt.Errorf("connector.CreateIntercept: %w", err)
		}
		ii = r.InterceptInfo
	}
	ae, err := createEngagement(ud, e, sftpPort)
	if err != nil {
		return nil, err
	}
	ae.assignEnvAndCreateMounts(ii.Environment, ii.Mounts, t)
	return ae, nil
}

func deactivateIntercept(e workloadExtension) error {
	ctx := context.WithoutCancel(e.connection().Context)
	ud := daemon.MustGetUserClient(ctx)
	ic, err := ud.GetIntercept(ctx, &manager.GetInterceptRequest{Name: e.name()})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			err = nil
		}
		return err
	}
	return intercept.Result(ud.RemoveIntercept(ctx, &manager.RemoveInterceptRequest2{Name: ic.Spec.Name}))
}
