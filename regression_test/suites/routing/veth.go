package routing

import (
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// vethNames holds one veth-up/veth-down cycle's interface names, randomized
// per invocation: integration_test/testdata/scripts/veth-up.sh hard-codes
// vm1/vm2/tapm/brm, so two overlapping runs (or a leftover from a prior
// failed one) collide (docs/plans/regression-test-framework/m3-wave4-spec.md's
// veth mechanism notes, "Naming collision risk").
type vethNames struct {
	veth1, veth2, tap, bridge string
}

// newVethNames derives interface names unique to this call, truncated to
// fit Linux's interface name limit (IFNAMSIZ - 1 == 15 bytes).
func newVethNames() vethNames {
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	return vethNames{
		veth1:  "vm1" + suffix,
		veth2:  "vm2" + suffix,
		tap:    "tp" + suffix,
		bridge: "br" + suffix,
	}
}

// conflictAddrs returns the ".1" (bridge) and ".2" (veth2) addresses inside
// cidr: cidr's network address with its last octet set to 1 and 2
// respectively, matching veth-up.sh's ".0"->".1"/".2" address rewrite. Only
// IPv4 is supported: neither this area nor the framework at large handles
// IPv6 clusters yet.
func conflictAddrs(cidr netip.Prefix) (bridge, veth2 netip.Prefix, err error) {
	addr := cidr.Addr()
	if !addr.Is4() {
		return netip.Prefix{}, netip.Prefix{}, fmt.Errorf("conflictAddrs: %s is not an IPv4 CIDR", cidr)
	}
	b := addr.As4()
	b[3] = 1
	bridge = netip.PrefixFrom(netip.AddrFrom4(b), cidr.Bits())
	b[3] = 2
	veth2 = netip.PrefixFrom(netip.AddrFrom4(b), cidr.Bits())
	return bridge, veth2, nil
}

// runSudo runs args under sudo, wrapping combined output into err on
// failure.
func runSudo(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, "sudo", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sudo %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return nil
}

// vethUp brings up a veth pair (n.veth1/n.veth2), a tap device (n.tap), and
// a bridge (n.bridge) enslaving both, then assigns an address inside cidr to
// the bridge and another to n.veth2: a local interface now owns an address
// inside cidr, so the kernel routing table gets a local, non-cluster route
// for it. Reproduces veth-up.sh's effect (see the spec's veth mechanism
// notes) with randomized interface names instead of the script's hard-coded
// ones.
func vethUp(ctx context.Context, cidr string, n vethNames) error {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return fmt.Errorf("vethUp: parsing %s: %w", cidr, err)
	}
	bridgeAddr, veth2Addr, err := conflictAddrs(prefix)
	if err != nil {
		return err
	}
	steps := [][]string{
		{"ip", "link", "add", "dev", n.veth1, "type", "veth", "peer", "name", n.veth2},
		{"ip", "link", "set", "dev", n.veth1, "up"},
		{"ip", "tuntap", "add", n.tap, "mode", "tap"},
		{"ip", "link", "set", "dev", n.tap, "up"},
		{"ip", "link", "add", n.bridge, "type", "bridge"},
		{"ip", "link", "set", n.tap, "master", n.bridge},
		{"ip", "link", "set", n.veth1, "master", n.bridge},
		{"ip", "addr", "add", bridgeAddr.String(), "dev", n.bridge},
		{"ip", "addr", "add", veth2Addr.String(), "dev", n.veth2},
		{"ip", "link", "set", n.bridge, "up"},
		{"ip", "link", "set", n.veth2, "up"},
	}
	for _, args := range steps {
		if err := runSudo(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

// vethDown reverses vethUp, exactly mirroring veth-down.sh's role for
// cidrConflictSuite. Errors are logged, not returned: this is meant to run
// as a defer after a possibly-partial vethUp, and every step must still be
// attempted (see the spec's veth mechanism notes: "a --ignore-errors-style
// caller isn't provided").
func vethDown(ctx context.Context, r *rt.Runtime, cidr string, n vethNames) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		r.Infof("[rtest] routing: veth-down: parsing %s: %v", cidr, err)
		return
	}
	bridgeAddr, veth2Addr, err := conflictAddrs(prefix)
	if err != nil {
		r.Infof("[rtest] routing: veth-down: %v", err)
		return
	}
	steps := [][]string{
		{"ip", "link", "set", n.veth2, "down"},
		{"ip", "link", "set", n.bridge, "down"},
		{"ip", "addr", "del", bridgeAddr.String(), "dev", n.bridge},
		{"ip", "addr", "del", veth2Addr.String(), "dev", n.veth2},
		{"ip", "link", "del", n.bridge, "type", "bridge"},
		{"ip", "link", "set", "dev", n.tap, "down"},
		{"ip", "tuntap", "del", n.tap, "mode", "tap"},
		{"ip", "link", "set", "dev", n.veth1, "down"},
		{"ip", "link", "del", "dev", n.veth1, "type", "veth", "peer", "name", n.veth2},
	}
	for _, args := range steps {
		if err := runSudo(ctx, args...); err != nil {
			r.Infof("[rtest] routing: veth-down %s: %v", strings.Join(args, " "), err)
		}
	}
}
