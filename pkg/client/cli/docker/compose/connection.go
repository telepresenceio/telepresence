package compose

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	compose "github.com/compose-spec/compose-go/v2/types"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type connectionConfig struct {
	// Namespace to connect to.
	Name             string         `json:"name,omitempty"`
	Namespace        string         `json:"namespace,omitempty"`
	AlsoProxy        []netip.Prefix `json:"also-proxy,omitempty"`
	NeverProxy       []netip.Prefix `json:"never-proxy,omitempty"`
	ManagerNamespace string         `json:"manager-namespace,omitempty"`
	MappedNamespaces []string       `json:"mapped-namespaces,omitempty"`
}

type connection struct {
	context.Context
	*connectionConfig
	subnets []netip.Prefix
	proxies map[string]netip.Addr
}

func composePortString(p *compose.ServicePortConfig) string {
	b := strings.Builder{}
	if p.HostIP != "" {
		b.WriteString(p.HostIP)
		b.WriteByte(':')
	}
	b.WriteString(p.Published)
	b.WriteByte(':')
	b.WriteString(strconv.Itoa(int(p.Target)))
	if p.Protocol != "" {
		b.WriteByte('/')
		b.WriteString(p.Protocol)
	}
	return b.String()
}

func composePortTarget(p *compose.ServicePortConfig) types.PortAndProto {
	pp := types.PortAndProto{Port: uint16(p.Target)}
	pp.Proto, _ = types.ParseProto(p.Protocol)
	return pp
}

func (cc *connectionConfig) Connect(ctx context.Context, es map[string]serviceExtension, mustPreExist bool) (*connection, error) {
	cr := daemon.GetRequest(ctx)
	cr = cr.Clone()
	cr.Implicit = false
	cr.Docker = true
	if cc.Namespace != "default" {
		cr.KubeFlags["namespace"] = cc.Namespace
	}
	cr.Name = cc.Name
	if cr.Name == "" {
		cr.Name = "trn-" + cc.Namespace
	}
	cr.ManagerNamespace = cc.ManagerNamespace
	if l := len(cc.AlsoProxy); l > 0 {
		cr.AlsoProxy = make([]string, l)
		for i, p := range cc.AlsoProxy {
			cr.AlsoProxy[i] = p.String()
		}
	}
	if l := len(cc.NeverProxy); l > 0 {
		cr.NeverProxy = make([]string, l)
		for i, p := range cc.NeverProxy {
			cr.NeverProxy[i] = p.String()
		}
	}
	cr.MappedNamespaces = cc.MappedNamespaces

	for _, e := range es {
		if e.engagementType() == types.EngagementTypeProxy && (e.connectionName() == "" || e.connectionName() == cc.Name) {
			err := addProxyReroutes(e.(servicePortExtension), cr)
			if err != nil {
				return nil, err
			}
		}
	}

	ctx = daemon.WithRequest(ctx, cr)
	ctx, err := connect.EnsureUserDaemon(ctx, !mustPreExist)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			connect.Disconnect(ctx)
		}
	}()

	ctx, err = connect.EnsureSession(ctx, "compose", true)
	if err != nil {
		return nil, err
	}
	ds := daemon.GetSession(ctx)
	rootCfg, err := daemon.GetRootClientConfig(daemon.GetSession(ctx).Info.DaemonStatus)
	if err != nil {
		dlog.Errorf(ctx, "unable to obtain routing info for connection: %v", err)
	}

	proxies, err := cc.resolveProxies(ctx, ds, es)
	if err != nil {
		return nil, err
	}
	return &connection{Context: ctx, connectionConfig: cc, proxies: proxies, subnets: rootCfg.Routing().Subnets}, nil
}

// resolveProxies resolves the name of the proxy definition into its remote service IP. This IP will then be
// made available to other compose services using `extra_hosts: ["<proxied service name>=<IP>"]`, so that any
// reference to the original service instead goes to the remote IP.
func (cc *connectionConfig) resolveProxies(ctx context.Context, ds *daemon.Session, es map[string]serviceExtension) (map[string]netip.Addr, error) {
	var proxies map[string]netip.Addr
	for _, e := range es {
		if e.engagementType() == types.EngagementTypeProxy && (e.connectionName() == "" || e.connectionName() == cc.Name) {
			ip, err := ds.Lookup(ctx, e.name())
			if err != nil {
				return nil, err
			}
			cn := e.composeService().Name
			if proxies == nil {
				proxies = map[string]netip.Addr{cn: ip}
			} else {
				proxies[cn] = ip
			}
		}
	}
	return proxies, nil
}

func (c *connection) disconnect() {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c), 3*time.Second)
	defer cancel()
	connect.Disconnect(ctx)
}

func addProxyReroutes(e servicePortExtension, cr *daemon.Request) error {
	addProxyLocalReroutes(e, cr)
	return addProxyRemoteReroutes(e, cr)
}

// addProxyLocalReroutes ensures that the ports published in the docker compose file for a proxied service are published
// by the Telepresence daemon container that facilitates the proxy. This is done using local reroutes.
//
// What's added here are essentially `--reroute-local <local port>:<host>:<remote port>` flags.
func addProxyLocalReroutes(e servicePortExtension, cr *daemon.Request) {
	ports := e.composeService().Ports
	for pi := range ports {
		p := &ports[pi]
		cr.ExposedPorts = append(cr.ExposedPorts, composePortString(p))
		cpt := composePortTarget(p)
		cr.LocalReroutes = append(cr.LocalReroutes, fmt.Sprintf("%d:%s:%d", cpt.Port, e.name(), cpt.Port))
	}
}

// addProxyRemoteReroutes ensures that the Telepresence daemon container reroutes the container ports of a proxy to
// their corresponding remote service ports. Other containers will reach this service using DNS, so a lookup for
// `<service-name>:<container-port>` must be rerouted to `<service-name>:<service-port>`.
//
// What's added here are essentially `--reroute-remote <host>:<remote port>:<new port>` flags.
func addProxyRemoteReroutes(e servicePortExtension, cr *daemon.Request) error {
	b := strings.Builder{}
	for _, p := range e.servicePorts() {
		_, s, _ := p.From().ProtoAndNameOrNumber()
		if s != "" {
			return fmt.Errorf("port %s is not a number in proxy for %s", s, e.composeService().Name)
		}
		b.Reset()
		b.WriteString(e.name())
		b.WriteByte(':')
		pis := p.ToAsIntOrStr()
		b.WriteString(pis.String())
		b.WriteByte(':')
		b.WriteString(p.From().String())
		cr.RemoteReroutes = append(cr.RemoteReroutes, b.String())
	}
	return nil
}
