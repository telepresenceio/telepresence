package compose

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	compose "github.com/compose-spec/compose-go/v2/types"
	"github.com/puzpuzpuz/xsync/v4"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type engagement struct {
	serviceExtension
	environment map[string]string
	daemonID    *daemon.Identifier
	sftpPort    uint16
	daemonIP    netip.Addr
}

func createEngagement(ud daemon.UserClient, e serviceExtension) (*engagement, error) {
	ae := &engagement{
		serviceExtension: e,
		daemonID:         ud.DaemonID(),
		daemonIP:         ud.DaemonInfo().ContainerIP,
	}
	if e.needsVolumes() {
		lma, err := client.FreePortsTCP(1)
		if err != nil {
			return nil, err
		}
		ae.sftpPort = lma[0].Port()
	}
	return ae, nil
}

func (a *engagement) assignEnvAndCreateMounts(remoteEnv map[string]string, remoteMounts map[string]int32, tpVolumes *xsync.Map[string, *compose.VolumeConfig]) {
	env := make(map[string]string)
	maps.Merge(env, remoteEnv)
	a.environment = env
	we, ok := a.serviceExtension.(mountsExtension)
	if !ok {
		return
	}
	mounts, serviceVolumes := we.desiredRemoteMounts(types.MountPoliciesFromRPC(remoteMounts))
	if len(mounts) == 0 {
		return
	}
	ro := a.engagementType() == types.EngagementTypeIngest || a.engagementType() == types.EngagementTypeWiretap
	ctx := a.connection().Context
	dlog.Debugf(ctx, "mounts: %v, ro %t", mounts, ro)
	createVolumes(ctx, netip.AddrPortFrom(a.daemonIP, a.sftpPort), a.environment["TELEPRESENCE_CONTAINER"], mounts, serviceVolumes, ro, tpVolumes)
}

const connectionAnnotationPrefix = "telepresence.io/connection-"

func (a *engagement) maybeAddConnection(s *compose.ServiceConfig) bool {
	conn := a.connection()
	cn := conn.Name
	if cn == "" {
		cn = "default"
	}
	myKey := connectionAnnotationPrefix + cn
	for k, sn := range s.Annotations {
		if strings.HasPrefix(k, connectionAnnotationPrefix) {
			if k == myKey {
				// Never add the same connection twice.
				return false
			}
			var subnets []netip.Prefix
			_ = client.UnmarshalJSON([]byte(sn), &subnets, true)
			for _, osn := range subnets {
				for _, msn := range conn.subnets {
					if osn.Overlaps(msn) {
						dlog.Warnf(conn, "Subnet %s in connection %s overlaps with subnet %s in connection %s. This prevents it from being added to service %s",
							msn, cn, osn, strings.TrimPrefix(k, connectionAnnotationPrefix), s.Name)
						return false
					}
				}
			}
		}
	}
	if s.Annotations == nil {
		s.Annotations = make(map[string]string)
	}
	myAnn, _ := client.MarshalJSON(conn.subnets)
	s.Annotations[myKey] = string(myAnn)
	if s.Networks == nil {
		s.Networks = make(map[string]*compose.ServiceNetworkConfig)
	}
	s.Networks[a.daemonID.Name] = nil
	return true
}

func (a *engagement) engageService(s *compose.ServiceConfig) {
	a.maybeAddConnection(s)
	env := a.environment
	if len(env) > 0 {
		if s.Environment == nil {
			s.Environment = make(compose.MappingWithEquals, len(env))
		}
		for k, v := range env {
			// Don't overwrite the value declared in the compose-spec.
			if _, ok := s.Environment[k]; !ok {
				s.Environment[k] = &v
			}
		}
	}
}

func (a *engagement) engageProxyDependents(p *compose.Project, n string, dependents []string) {
	conn := a.connection()
	sm := p.Services

	for _, d := range dependents {
		// Dependent services need a new DNS for the proxy, and also the network
		// that routes its IP.
		ds := sm[d]
		if !a.maybeAddConnection(&ds) {
			continue
		}
		if len(conn.proxies) > 0 {
			extraHosts := make(map[string][]string, len(conn.proxies))
			for name, ip := range conn.proxies {
				extraHosts[name] = []string{ip.String()}
			}
			if ds.ExtraHosts != nil {
				maps.Merge(ds.ExtraHosts, extraHosts)
			} else {
				ds.ExtraHosts = extraHosts
			}
		}
		if ipS := a.daemonIP.String(); !slices.Contains(ds.DNS, ipS) {
			ds.DNS = append(ds.DNS, ipS)
		}
		// A proxied service is simply removed from the compose-spec along with any dependents.
		delete(ds.DependsOn, n)
		sm[d] = ds
	}
}

func (a *engagement) engageProject(p *compose.Project) {
	if p.Networks == nil {
		p.Networks = make(compose.Networks)
	}
	nn := a.daemonID.Name
	if _, ok := p.Networks[nn]; !ok {
		p.Networks[nn] = compose.NetworkConfig{
			Name:     nn,
			External: true,
		}
	}
}

// createVolumes creates the VolumeConfigs necessary when mounting volumes required by when engaging a remote container.
// The hostPort is the <daemon ip>/<sftp port> where the access to the remote sftp-server is provided.
// The mounts are provided as a map of mount policies keyed by paths.
// Each volume is given the name of the remote container suffixed by a dash and a sequence number, starting at 1.
// Returns a map of VolumeConfig keyed by volume target paths.
func createVolumes(
	ctx context.Context,
	hostPort netip.AddrPort,
	remoteContainer string,
	mounts types.MountPolicies,
	serviceVolumes map[string]*compose.ServiceVolumeConfig,
	ro bool,
	vols *xsync.Map[string, *compose.VolumeConfig],
) {
	var plugin string
	i := 0
	for dir, policy := range mounts {
		sv := serviceVolumes[dir]
		volRO := ro || sv.ReadOnly
		switch policy {
		case types.MountPolicyIgnore, types.MountPolicyLocal:
			continue
		case types.MountPolicyRemoteReadOnly:
			volRO = true
		default:
		}
		var err error
		if plugin == "" {
			plugin, err = docker.EnsureVolumePlugin(ctx)
			if err != nil {
				ioutil.Printf(output.Err(ctx), "Remote mount disabled: %s\n", err)
				return
			}
		}
		vols.Compute(sv.Source, func(prev *compose.VolumeConfig, loaded bool) (vol *compose.VolumeConfig, op xsync.ComputeOp) {
			if loaded {
				if !volRO && prev.DriverOpts["ro"] == "true" {
					// A read-write volume is needed by this service, so we can't use a read-only one.
					delete(prev.DriverOpts, "ro")
				}
				return nil, xsync.CancelOp
			}
			i++
			return createVolume(ctx, plugin, hostPort, fmt.Sprintf("%s-%d", remoteContainer, i), remoteContainer, dir, volRO), xsync.UpdateOp
		})
	}
}

func createVolume(ctx context.Context, pluginName string, hostPort netip.AddrPort, volumeName, container, dir string, ro bool) *compose.VolumeConfig {
	return &compose.VolumeConfig{
		Name:       volumeName,
		Driver:     pluginName,
		DriverOpts: docker.VolumeDriverOpts(ctx, pluginName, hostPort, volumeName, container, dir, ro),
	}
}
