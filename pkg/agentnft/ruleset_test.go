//go:build linux

package agentnft

import (
	"bytes"
	"net/netip"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"

	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// outputGate is the decoded shape of an OUTPUT redirect gate, used to assert
// all three agentinit cases are reproduced.
type outputGate struct {
	oifLo         bool
	daddrPodIP    bool // ip daddr == podIP
	daddrNotLocal bool // ip daddr != localhost
	hasOwner      bool
	ownerEq       bool // meaningful only when hasOwner; false => != owner
}

func basicConfig() Config {
	return Config{
		PodIP:    netip.MustParseAddr("10.42.0.5"),
		Loopback: "lo",
		Owner:    OwnerMatch{UseGID: true, ID: 7439},
		Intercepts: []Intercept{
			{Protocol: types.ProtoTCP, ContainerPort: 8080, AgentPort: 9090},
		},
	}
}

func TestBuildValidatesConfig(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"valid", func(*Config) {}, false},
		{"missing pod IP", func(c *Config) { c.PodIP = netip.Addr{} }, true},
		{"missing loopback", func(c *Config) { c.Loopback = "" }, true},
		{"no intercepts is allowed (mesh bypass only)", func(c *Config) { c.Intercepts = nil }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := basicConfig()
			tt.mutate(&cfg)
			_, err := Build(cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Build() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBuildTableFamily(t *testing.T) {
	tests := []struct {
		name       string
		podIP      string
		wantFamily nftables.TableFamily
	}{
		{"IPv4", "10.42.0.5", nftables.TableFamilyIPv4},
		{"IPv6", "fd00::5", nftables.TableFamilyIPv6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := basicConfig()
			cfg.PodIP = netip.MustParseAddr(tt.podIP)
			rs, err := Build(cfg)
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if rs.Table.Family != tt.wantFamily {
				t.Errorf("Table.Family = %v, want %v", rs.Table.Family, tt.wantFamily)
			}
			if rs.Table.Name != TableName {
				t.Errorf("Table.Name = %q, want %q", rs.Table.Name, TableName)
			}
		})
	}
}

func TestBuildChainHooksAndPriorities(t *testing.T) {
	rs, err := Build(basicConfig())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if rs.Prerouting.Type != nftables.ChainTypeNAT || rs.Output.Type != nftables.ChainTypeNAT {
		t.Errorf("chains must be nat type, got %v/%v", rs.Prerouting.Type, rs.Output.Type)
	}
	if *rs.Prerouting.Hooknum != *nftables.ChainHookPrerouting {
		t.Errorf("Prerouting.Hooknum = %v, want prerouting", *rs.Prerouting.Hooknum)
	}
	if *rs.Output.Hooknum != *nftables.ChainHookOutput {
		t.Errorf("Output.Hooknum = %v, want output", *rs.Output.Hooknum)
	}
	if len(rs.Chains) != 2 || rs.Chains[0] != rs.Prerouting || rs.Chains[1] != rs.Output {
		t.Errorf("Chains = %+v, want [Prerouting, Output]", rs.Chains)
	}

	meshDstnat := int32(*nftables.ChainPriorityNATDest)
	pre := int32(*rs.Prerouting.Priority)
	out := int32(*rs.Output.Priority)

	// prerouting must run AFTER the mesh's dstnat (so a mesh's inbound redirect
	// wins); output must run BEFORE it (so our nat decisions preempt the mesh's
	// outbound redirect).
	if pre <= meshDstnat {
		t.Errorf("prerouting priority %d must be > mesh dstnat %d (run after)", pre, meshDstnat)
	}
	if out >= meshDstnat {
		t.Errorf("output priority %d must be < mesh dstnat %d (run before)", out, meshDstnat)
	}
	if pre != DefaultPreroutingPriority {
		t.Errorf("Prerouting.Priority = %d, want default %d", pre, DefaultPreroutingPriority)
	}
	if out != DefaultOutputPriority {
		t.Errorf("Output.Priority = %d, want default %d", out, DefaultOutputPriority)
	}
}

func TestBuildRedirectMapCoversBothProtocols(t *testing.T) {
	cfg := basicConfig()
	cfg.Intercepts = []Intercept{
		{Protocol: types.ProtoTCP, ContainerPort: 8080, AgentPort: 9090},
		{Protocol: types.ProtoTCP, ContainerPort: 8081, AgentPort: 9091},
		{Protocol: types.ProtoUDP, ContainerPort: 8080, AgentPort: 9092},
	}
	rs, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if rs.RedirectMap == nil {
		t.Fatal("expected a redirect map")
	}
	m := rs.RedirectMap.Set
	if m.Name != mapRedirectPorts {
		t.Errorf("map name = %q, want %q", m.Name, mapRedirectPorts)
	}
	if !m.IsMap {
		t.Error("redirect map must be a map (IsMap)")
	}
	if !m.Concatenation {
		t.Error("redirect map must set Concatenation (required for a concat key, or `nft list ruleset` segfaults)")
	}
	if m.KeyType.Bytes != 8 {
		t.Errorf("KeyType.Bytes = %d, want 8 (inet_proto padded to 4 + inet_service padded to 4)", m.KeyType.Bytes)
	}
	if m.DataType != nftables.TypeInetService {
		t.Errorf("DataType = %v, want inet_service", m.DataType)
	}
	// TCP and UDP intercepts on the same container port land in the same map,
	// each keyed by its own protocol, with the correct agent port.
	assertRedirectMapElements(t, rs.RedirectMap.Elements, map[protoPort]uint16{
		{types.ProtoTCP, 8080}: 9090,
		{types.ProtoTCP, 8081}: 9091,
		{types.ProtoUDP, 8080}: 9092,
	})
	if len(rs.Sets) == 0 || rs.Sets[0] != rs.RedirectMap {
		t.Errorf("Sets = %+v, want RedirectMap first", rs.Sets)
	}
}

func TestBuildRedirectMapElementKeyBytes(t *testing.T) {
	cfg := basicConfig()
	cfg.Intercepts = []Intercept{{Protocol: types.ProtoTCP, ContainerPort: 8080, AgentPort: 9090}}
	rs, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(rs.RedirectMap.Elements) != 1 {
		t.Fatalf("len(Elements) = %d, want 1", len(rs.RedirectMap.Elements))
	}
	e := rs.RedirectMap.Elements[0]
	wantKey := []byte{byte(types.ProtoTCP), 0, 0, 0, 0x1F, 0x90, 0, 0} // 8080 = 0x1F90
	if !bytes.Equal(e.Key, wantKey) {
		t.Errorf("element key = %x, want %x", e.Key, wantKey)
	}
	wantVal := []byte{0x23, 0x82} // 9090 = 0x2382
	if !bytes.Equal(e.Val, wantVal) {
		t.Errorf("element val = %x, want %x", e.Val, wantVal)
	}
}

func TestBuildNoRedirectMapWithoutIntercepts(t *testing.T) {
	cfg := basicConfig()
	cfg.Intercepts = nil
	rs, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if rs.RedirectMap != nil {
		t.Error("did not expect a redirect map when there are no intercepts")
	}
	for _, r := range rs.Rules {
		if r.Chain == rs.Prerouting {
			t.Error("did not expect any prerouting rule without intercepts")
		}
		if r.Chain == rs.Output && ruleHasRedir(r) {
			t.Error("did not expect any output redirect gate without intercepts")
		}
	}
	// The mesh bypass rule is unconditional.
	var bypassCount int
	for _, r := range rs.Rules {
		if r.Chain == rs.Output && ruleIsIdentityDNAT(r) {
			bypassCount++
		}
	}
	if bypassCount != 1 {
		t.Errorf("bypass rule count = %d, want 1", bypassCount)
	}
}

func TestBuildDedupsRepeatedProtoPort(t *testing.T) {
	cfg := basicConfig()
	cfg.Intercepts = []Intercept{
		{Protocol: types.ProtoTCP, ContainerPort: 8080, AgentPort: 9090},
		{Protocol: types.ProtoTCP, ContainerPort: 8080, AgentPort: 9099}, // duplicate (proto, cport)
	}
	rs, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	// First agent port wins, and only one element is emitted (a duplicate map
	// key would be rejected by the kernel).
	assertRedirectMapElements(t, rs.RedirectMap.Elements, map[protoPort]uint16{
		{types.ProtoTCP, 8080}: 9090,
	})
}

func TestBuildPreroutingHasExactlyOneRule(t *testing.T) {
	rs, err := Build(basicConfig())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	var n int
	for _, r := range rs.Rules {
		if r.Chain == rs.Prerouting {
			n++
		}
	}
	if n != 1 {
		t.Errorf("prerouting rule count = %d, want 1", n)
	}
}

// TestBuildOutputRedirectGates verifies all three OUTPUT redirect gates are
// present with the right owner-match sense and destination gating.
func TestBuildOutputRedirectGates(t *testing.T) {
	rs, err := Build(basicConfig())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	want := []outputGate{
		{oifLo: true, hasOwner: true, ownerEq: false},                     // 1: non-agent via loopback
		{daddrPodIP: true, hasOwner: false},                               // 2: any traffic via podIP, no owner match
		{oifLo: true, daddrNotLocal: true, hasOwner: true, ownerEq: true}, // 3: agent via loopback, not localhost
	}

	var got []outputGate
	for _, r := range rs.Rules {
		if r.Chain != rs.Output || !ruleHasRedir(r) {
			continue
		}
		got = append(got, classifyGate(t, r))
	}
	if len(got) != len(want) {
		t.Fatalf("found %d redirect gates, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("gate %d = %+v, want %+v", i, got[i], w)
		}
	}
}

// TestRedirectViaProtoPortMapExprs pins down the exact expression sequence
// emitted for a redirect lookup: l4proto into reg 1, the transport dport into
// NFT_REG32_01 (the register immediately after reg 1's first word), a
// concat-keyed map lookup, then a redirect.
func TestRedirectViaProtoPortMapExprs(t *testing.T) {
	set := &nftables.Set{Name: mapRedirectPorts, ID: 3}
	exprs := redirectViaProtoPortMap(set)
	if len(exprs) != 4 {
		t.Fatalf("len(exprs) = %d, want 4", len(exprs))
	}
	meta, ok := exprs[0].(*expr.Meta)
	if !ok || meta.Key != expr.MetaKeyL4PROTO || meta.Register != 1 {
		t.Errorf("exprs[0] = %+v, want meta l4proto -> reg 1", exprs[0])
	}
	payload, ok := exprs[1].(*expr.Payload)
	if !ok || payload.Base != expr.PayloadBaseTransportHeader || payload.Offset != 2 || payload.Len != 2 {
		t.Fatalf("exprs[1] = %+v, want transport-header dport load", exprs[1])
	}
	if payload.DestRegister != unix.NFT_REG32_01 {
		t.Errorf("dport DestRegister = %d, want NFT_REG32_01 (%d)", payload.DestRegister, unix.NFT_REG32_01)
	}
	lookup, ok := exprs[2].(*expr.Lookup)
	if !ok || lookup.SourceRegister != 1 || !lookup.IsDestRegSet || lookup.DestRegister != 1 {
		t.Fatalf("exprs[2] = %+v, want a dest-set lookup keyed from reg 1 into reg 1", exprs[2])
	}
	if lookup.SetName != set.Name || lookup.SetID != set.ID {
		t.Errorf("lookup set = %q/%d, want %q/%d", lookup.SetName, lookup.SetID, set.Name, set.ID)
	}
	redir, ok := exprs[3].(*expr.Redir)
	if !ok || redir.RegisterProtoMin != 1 {
		t.Fatalf("exprs[3] = %+v, want redirect from reg 1", exprs[3])
	}
}

func TestBuildProxyPortDNAT(t *testing.T) {
	cfg := basicConfig()
	cfg.Intercepts[0].ProxyPort = 9191
	rs, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	var dnat *nftables.Rule
	for _, r := range rs.Rules {
		// The proxy DNAT ends in a NAT expr but is NOT the identity DNAT: it
		// carries two Immediates (podIP, containerPort).
		if r.Chain == rs.Output && ruleImmediateCount(r) == 2 {
			dnat = r
		}
	}
	if dnat == nil {
		t.Fatal("no proxy-port DNAT rule found")
	}
	n := len(dnat.Exprs)
	nat, ok := dnat.Exprs[n-1].(*expr.NAT)
	if !ok || nat.Type != expr.NATTypeDestNAT {
		t.Fatalf("proxy DNAT does not end in a dest NAT: %T", dnat.Exprs[n-1])
	}
	if nat.Family != uint32(rs.Table.Family) {
		t.Errorf("NAT.Family = %d, want %d", nat.Family, rs.Table.Family)
	}
	addrImm, _ := dnat.Exprs[n-3].(*expr.Immediate)
	portImm, _ := dnat.Exprs[n-2].(*expr.Immediate)
	if addrImm == nil || portImm == nil {
		t.Fatalf("proxy DNAT missing address/port immediates")
	}
	if !bytes.Equal(addrImm.Data, cfg.PodIP.AsSlice()) {
		t.Errorf("proxy DNAT address = %x, want podIP %x", addrImm.Data, cfg.PodIP.AsSlice())
	}
	if got := binaryutil.BigEndian.Uint16(portImm.Data); got != cfg.Intercepts[0].ContainerPort {
		t.Errorf("proxy DNAT port = %d, want containerPort %d", got, cfg.Intercepts[0].ContainerPort)
	}
}

func TestBuildNoProxyPortDNATForSymbolicTarget(t *testing.T) {
	rs, err := Build(basicConfig()) // ProxyPort == 0
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, r := range rs.Rules {
		if ruleImmediateCount(r) == 2 {
			t.Errorf("unexpected proxy-port DNAT rule for a symbolic target intercept")
		}
	}
}

// TestBuildIdentityDNATBypass verifies the agent's general-egress bypass: it
// is a single identity DNAT (dnat to daddr, no immediates, no l4proto match)
// that matches the agent owner, excludes DNS, and -- when mesh subnets exist
// -- excludes them via an inverted set lookup.
func TestBuildIdentityDNATBypass(t *testing.T) {
	for _, withMesh := range []bool{false, true} {
		name := "no mesh subnets"
		if withMesh {
			name = "with mesh subnets"
		}
		t.Run(name, func(t *testing.T) {
			cfg := basicConfig()
			if withMesh {
				cfg.MeshDialSubnets = []netip.Prefix{netip.MustParsePrefix("10.96.0.0/12")}
			}
			rs, err := Build(cfg)
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}

			var bypass []*nftables.Rule
			for _, r := range rs.Rules {
				if r.Chain == rs.Output && ruleIsIdentityDNAT(r) {
					bypass = append(bypass, r)
				}
			}
			if len(bypass) != 1 {
				t.Fatalf("found %d identity-DNAT bypass rules, want 1", len(bypass))
			}
			r := bypass[0]
			assertOwnerEq(t, r, cfg.Owner)
			if !ruleExcludesPort(t, r, dnsPort) {
				t.Errorf("bypass rule does not exclude DNS (port 53)")
			}
			if ruleHasL4ProtoMatch(r) {
				t.Errorf("bypass rule must not match l4proto")
			}
			sawSetLookup := ruleHasInvertedSetLookup(r)
			if sawSetLookup != withMesh {
				t.Errorf("inverted mesh-set lookup present = %v, want %v", sawSetLookup, withMesh)
			}
		})
	}
}

func TestBuildMeshSetOnlyMatchingFamily(t *testing.T) {
	cfg := basicConfig() // IPv4 pod
	cfg.MeshDialSubnets = []netip.Prefix{
		netip.MustParsePrefix("240.240.0.0/16"),
		netip.MustParsePrefix("fd00:240::/32"), // wrong family, must be dropped
	}
	rs, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if rs.MeshSet == nil {
		t.Fatal("expected a mesh set for the IPv4 subnet")
	}
	if rs.MeshSet.Set.KeyType != nftables.TypeIPAddr {
		t.Errorf("mesh set key type = %v, want ipv4_addr", rs.MeshSet.Set.KeyType)
	}
	// The /16 yields a start element and an end marker; the IPv6 subnet must
	// contribute nothing.
	for _, e := range rs.MeshSet.Elements {
		if len(e.Key) != 4 {
			t.Errorf("mesh set element key is not a 4-byte IPv4 address: %x", e.Key)
		}
	}
}

func TestBuildNoMeshSetWhenNoMatchingFamily(t *testing.T) {
	cfg := basicConfig() // IPv4 pod
	cfg.MeshDialSubnets = []netip.Prefix{netip.MustParsePrefix("fd00::/16")}
	rs, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if rs.MeshSet != nil {
		t.Error("did not expect a mesh set when no subnet matches the pod family")
	}
	// And the identity-DNAT bypass must then carry no set lookup.
	for _, r := range rs.Rules {
		if r.Chain == rs.Output && ruleIsIdentityDNAT(r) && ruleHasInvertedSetLookup(r) {
			t.Error("bypass rule references a mesh set that should not exist")
		}
	}
}

func TestBuildOwnerUIDFallback(t *testing.T) {
	cfg := basicConfig()
	cfg.Owner = OwnerMatch{UseGID: false, ID: 1000}
	rs, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	// Every owner match in the ruleset must use skuid, never skgid.
	for _, r := range rs.Rules {
		for _, e := range r.Exprs {
			m, ok := e.(*expr.Meta)
			if !ok {
				continue
			}
			if m.Key == expr.MetaKeySKGID {
				t.Errorf("found meta skgid match, want skuid (UID fallback)")
			}
		}
	}
}

func TestBuildOutputPrecedence(t *testing.T) {
	cfg := basicConfig()
	cfg.Intercepts[0].ProxyPort = 9191
	cfg.MeshDialSubnets = []netip.Prefix{netip.MustParsePrefix("10.96.0.0/12")}
	rs, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	// Within the output chain: the three redirect gates, then the proxy DNAT,
	// then the identity DNAT. The identity DNAT must come last so agent
	// traffic to its own app/proxy ports is redirected/rewritten rather than
	// pinned direct.
	var redirCount int
	lastRedir, firstProxy, firstIdentity := -1, -1, -1
	i := 0
	for _, r := range rs.Rules {
		if r.Chain != rs.Output {
			continue
		}
		switch {
		case ruleHasRedir(r):
			lastRedir = i
			redirCount++
		case ruleIsIdentityDNAT(r):
			if firstIdentity == -1 {
				firstIdentity = i
			}
		case ruleImmediateCount(r) == 2:
			if firstProxy == -1 {
				firstProxy = i
			}
		}
		i++
	}
	if redirCount != 3 {
		t.Errorf("redirect gate count = %d, want 3", redirCount)
	}
	if !(lastRedir < firstProxy && firstProxy < firstIdentity) {
		t.Errorf("output precedence wrong: lastRedir=%d firstProxy=%d firstIdentity=%d", lastRedir, firstProxy, firstIdentity)
	}
}

// TestBuildOwnerMarkDiscriminator verifies that a non-zero OwnerMatch.Mark
// switches every owner-matching rule to `meta mark`, never skgid/skuid --
// the discriminator a node-agent must use for a target whose network
// namespace is owned by a non-init user namespace.
func TestBuildOwnerMarkDiscriminator(t *testing.T) {
	cfg := basicConfig()
	cfg.Owner = OwnerMatch{Mark: 0x2374}
	rs, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	var sawMark bool
	for _, r := range rs.Rules {
		for i, e := range r.Exprs {
			m, ok := e.(*expr.Meta)
			if !ok {
				continue
			}
			switch m.Key {
			case expr.MetaKeySKGID, expr.MetaKeySKUID:
				t.Errorf("found socket-owner match %v, want meta mark only", m.Key)
			case expr.MetaKeyMARK:
				sawMark = true
				cmp, ok := r.Exprs[i+1].(*expr.Cmp)
				if !ok {
					t.Fatalf("meta mark not followed by *expr.Cmp")
				}
				if !bytes.Equal(cmp.Data, binaryutil.NativeEndian.PutUint32(0x2374)) {
					t.Errorf("mark Cmp.Data = %x, want native-endian encoding %x", cmp.Data, binaryutil.NativeEndian.PutUint32(0x2374))
				}
			}
		}
	}
	if !sawMark {
		t.Error("no meta mark match found in ruleset")
	}
}

// TestMatchOwnerMarkTakesPrecedence verifies that matchOwner emits a mark
// match (and no skgid/skuid expressions) whenever owner.Mark is non-zero,
// regardless of UseGID/ID.
func TestMatchOwnerMarkTakesPrecedence(t *testing.T) {
	exprs := matchOwner(OwnerMatch{UseGID: true, ID: 7439, Mark: 0x2374}, false)
	if len(exprs) != 2 {
		t.Fatalf("matchOwner returned %d exprs, want 2", len(exprs))
	}
	m, ok := exprs[0].(*expr.Meta)
	if !ok || m.Key != expr.MetaKeyMARK {
		t.Fatalf("exprs[0] = %#v, want *expr.Meta{Key: MetaKeyMARK}", exprs[0])
	}
	cmp, ok := exprs[1].(*expr.Cmp)
	if !ok {
		t.Fatalf("exprs[1] is %T, want *expr.Cmp", exprs[1])
	}
	if !bytes.Equal(cmp.Data, binaryutil.NativeEndian.PutUint32(0x2374)) {
		t.Errorf("mark Cmp.Data = %x, want native-endian encoding %x", cmp.Data, binaryutil.NativeEndian.PutUint32(0x2374))
	}
}

func TestBuildSkgidByteOrderIsNative(t *testing.T) {
	// meta skgid/skuid, like meta mark, are host-endian nft datatypes: the Cmp
	// data must be produced with NativeEndian, not BigEndian, or the comparison
	// will silently never match on a little-endian host (the common case).
	exprs := matchOwner(OwnerMatch{UseGID: true, ID: 7439}, false)
	cmp, ok := exprs[1].(*expr.Cmp)
	if !ok {
		t.Fatalf("exprs[1] is %T, want *expr.Cmp", exprs[1])
	}
	if !bytes.Equal(cmp.Data, binaryutil.NativeEndian.PutUint32(7439)) {
		t.Errorf("owner Cmp.Data = %x, want native-endian encoding %x", cmp.Data, binaryutil.NativeEndian.PutUint32(7439))
	}
}

// --- helpers ---------------------------------------------------------------

func assertRedirectMapElements(t *testing.T, elems []nftables.SetElement, want map[protoPort]uint16) {
	t.Helper()
	if len(elems) != len(want) {
		t.Fatalf("map has %d elements, want %d", len(elems), len(want))
	}
	for _, e := range elems {
		if len(e.Key) != 8 {
			t.Fatalf("element key length = %d, want 8", len(e.Key))
		}
		k := protoPort{types.Proto(e.Key[0]), binaryutil.BigEndian.Uint16(e.Key[4:6])}
		v := binaryutil.BigEndian.Uint16(e.Val)
		if wv, ok := want[k]; !ok || wv != v {
			t.Errorf("map[%+v] = %d, want %d (present=%v)", k, v, want[k], ok)
		}
	}
}

func ruleHasRedir(r *nftables.Rule) bool {
	for _, e := range r.Exprs {
		if _, ok := e.(*expr.Redir); ok {
			return true
		}
	}
	return false
}

func ruleHasL4ProtoMatch(r *nftables.Rule) bool {
	for _, e := range r.Exprs {
		if m, ok := e.(*expr.Meta); ok && m.Key == expr.MetaKeyL4PROTO {
			return true
		}
	}
	return false
}

func ruleImmediateCount(r *nftables.Rule) int {
	n := 0
	for _, e := range r.Exprs {
		if _, ok := e.(*expr.Immediate); ok {
			n++
		}
	}
	return n
}

// ruleIsIdentityDNAT reports whether r ends in a dest NAT that has no immediate
// operands (i.e. `dnat to daddr`, as opposed to the proxy DNAT to a literal).
func ruleIsIdentityDNAT(r *nftables.Rule) bool {
	hasNAT := false
	for _, e := range r.Exprs {
		if nat, ok := e.(*expr.NAT); ok && nat.Type == expr.NATTypeDestNAT {
			hasNAT = true
		}
	}
	return hasNAT && ruleImmediateCount(r) == 0
}

func ruleHasInvertedSetLookup(r *nftables.Rule) bool {
	for _, e := range r.Exprs {
		if l, ok := e.(*expr.Lookup); ok && l.Invert {
			return true
		}
	}
	return false
}

func classifyGate(t *testing.T, r *nftables.Rule) outputGate {
	t.Helper()
	var g outputGate
	for i, e := range r.Exprs {
		switch v := e.(type) {
		case *expr.Meta:
			switch v.Key {
			case expr.MetaKeyOIFNAME:
				g.oifLo = true
			case expr.MetaKeySKGID, expr.MetaKeySKUID:
				g.hasOwner = true
				if cmp, ok := r.Exprs[i+1].(*expr.Cmp); ok {
					g.ownerEq = cmp.Op == expr.CmpOpEq
				}
			}
		case *expr.Cmp:
			// A daddr comparison follows a network-header payload load.
			if i == 0 {
				continue
			}
			if p, ok := r.Exprs[i-1].(*expr.Payload); ok && p.Base == expr.PayloadBaseNetworkHeader {
				if v.Op == expr.CmpOpNeq {
					g.daddrNotLocal = true
				} else {
					g.daddrPodIP = true
				}
			}
		}
	}
	return g
}

func assertOwnerEq(t *testing.T, r *nftables.Rule, owner OwnerMatch) {
	t.Helper()
	wantKey := expr.MetaKeySKGID
	if !owner.UseGID {
		wantKey = expr.MetaKeySKUID
	}
	for i, e := range r.Exprs {
		m, ok := e.(*expr.Meta)
		if !ok || (m.Key != expr.MetaKeySKGID && m.Key != expr.MetaKeySKUID) {
			continue
		}
		if m.Key != wantKey {
			t.Errorf("owner match uses %v, want %v", m.Key, wantKey)
		}
		cmp, ok := r.Exprs[i+1].(*expr.Cmp)
		if !ok || cmp.Op != expr.CmpOpEq {
			t.Errorf("owner match is not an equality against the agent id")
		}
		if !bytes.Equal(cmp.Data, binaryutil.NativeEndian.PutUint32(owner.ID)) {
			t.Errorf("owner id encoded as %x, want native-endian %x", cmp.Data, binaryutil.NativeEndian.PutUint32(owner.ID))
		}
		return
	}
	t.Errorf("rule has no owner match")
}

func ruleExcludesPort(t *testing.T, r *nftables.Rule, port uint16) bool {
	t.Helper()
	for i, e := range r.Exprs {
		p, ok := e.(*expr.Payload)
		if !ok || p.Base != expr.PayloadBaseTransportHeader || p.Offset != 2 {
			continue
		}
		cmp, ok := r.Exprs[i+1].(*expr.Cmp)
		if !ok {
			continue
		}
		if cmp.Op == expr.CmpOpNeq && bytes.Equal(cmp.Data, binaryutil.BigEndian.PutUint16(port)) {
			return true
		}
	}
	return false
}
