package quic

import (
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// nodeAddressesJSONPath extracts, per Node, the first ExternalIP address (if
// any) followed by a "|" and the first InternalIP address (if any): the same
// preference order the manager applies when it has no explicit
// quicTunnel.externalHost/externalPort override (cmd/traffic/cmd/manager/
// quictunnel/discovery.go's preferredNodeAddress).
const nodeAddressesJSONPath = `{range .items[*]}` +
	`{.status.addresses[?(@.type=="ExternalIP")].address}{"|"}` +
	`{.status.addresses[?(@.type=="InternalIP")].address}{"\n"}{end}`

// Discovery proves the zero-configuration discovery path: with
// managers.QuicNodePort() leaving quicTunnel.externalHost/externalPort
// unset, the traffic-manager pairs the cluster's Node addresses with the
// NodePort Service's own port itself (cmd/traffic/cmd/manager/quictunnel/
// discovery.go's candidatesForService/preferredNodeAddress -- ExternalIP
// preferred, InternalIP fallback, per Node), and the endpoint a connecting
// client lands on is one of those derived candidates. The manager reads
// Nodes through the chart's cluster-scoped RBAC (charts/telepresence-oss/
// templates/trafficManagerRbac/cluster-scope.yaml grants nodes get/list/
// watch). Every other suite in this area only prefix-matches "quic " (see
// helpers.go's quicPrefix); this one pins the endpoint to the discovered
// set instead.
type Discovery struct {
	rt.Suite
}

func init() {
	rt.Register(&Discovery{}, rt.InArea("quic"), rt.NeedsManager(managers.QuicNodePort()))
}

// Test_NodePortEndpointDiscovery connects until the quic transport is
// active, independently rebuilds the legitimate candidate endpoint set from
// the cluster's own Nodes, and requires the reported endpoint to be a member
// of that set.
func (s *Discovery) Test_NodePortEndpointDiscovery() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	tp := s.CLI()

	awaitTransportPrefix(t, ctx, tp, ns, freshConnect(t, ctx, ns))

	out, err := s.R().Kubectl(ctx, "", "get", "nodes", "-o", "jsonpath="+nodeAddressesJSONPath)
	if err != nil {
		t.Fatalf("get nodes: %v", err)
	}

	var wants []string
	for line := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		// Either side of the "|" may list several space-separated
		// addresses; the manager takes the first of the preferred type.
		external, internal, _ := strings.Cut(line, "|")
		host := ""
		if f := strings.Fields(external); len(f) > 0 {
			host = f[0]
		} else if f := strings.Fields(internal); len(f) > 0 {
			host = f[0]
		}
		if host == "" {
			continue
		}
		ep := net.JoinHostPort(host, strconv.Itoa(managers.QuicNodePortPort))
		wants = append(wants, "quic ("+ep+")")
	}
	s.Require().NotEmpty(wants, "expected at least one Node address to derive a candidate endpoint from")

	st := fetchStatus(t, ctx, tp)
	s.True(slices.Contains(wants, st.RootDaemon.TunnelTransport),
		"tunnel_transport %q should be one of the discovered candidates %v",
		st.RootDaemon.TunnelTransport, wants)
}
