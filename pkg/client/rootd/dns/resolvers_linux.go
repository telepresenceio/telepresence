package dns

import (
	"bytes"
	"context"
	"net/netip"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/telepresenceio/clog"
)

// SystemResolvers returns the DNS server addresses configured on the host,
// merged from /etc/resolv.conf and "resolvectl dns". It is used to detect a DNS
// server that would otherwise be routed into the cluster because its address is
// covered by a routed subnet.
//
// It is best-effort: an unreadable resolv.conf or a missing resolvectl yields no
// addresses from that source rather than an error, since failing to discover a
// resolver here only means the caller falls back to the explicitly configured
// addresses.
func SystemResolvers(ctx context.Context) []netip.AddrPort {
	aps, err := nameserversFromResolvConf(ctx)
	if err != nil {
		clog.Debugf(ctx, "unable to read /etc/resolv.conf when detecting local DNS servers: %v", err)
	}
	aps = append(aps, resolveCtlResolvers(ctx)...)
	slices.SortFunc(aps, compareAddrPort)
	return slices.Compact(aps)
}

func resolveCtlResolvers(ctx context.Context) []netip.AddrPort {
	// Bound the exec so a wedged resolvectl/systemd-resolved cannot stall the
	// reconcile (and therefore connect). DNS discovery is best-effort; on
	// timeout we simply fall back to the explicitly configured addresses.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "resolvectl", "dns").Output()
	if err != nil {
		clog.Debugf(ctx, "unable to discover DNS servers using resolvectl dns: %v", err)
		return nil
	}
	return parseResolveCtlDNS(out)
}

// parseResolveCtlDNS extracts the DNS server addresses from the output of
// "resolvectl dns". Each server is rendered by systemd as
// ADDRESS[:PORT][%IFACE][#SERVERNAME] (with IPv6 bracketed when a port is
// present), framed by "Global:" and "Link N (iface):" labels. The interface and
// server-name decorations are stripped and the remainder is parsed as an
// address with optional port, defaulting to port 53.
func parseResolveCtlDNS(out []byte) []netip.AddrPort {
	var aps []netip.AddrPort
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		for _, field := range strings.Fields(string(line)) {
			if ap, ok := parseResolveCtlServer(field); ok {
				aps = append(aps, ap)
			}
		}
	}
	slices.SortFunc(aps, compareAddrPort)
	return slices.Compact(aps)
}

func parseResolveCtlServer(field string) (netip.AddrPort, bool) {
	// Drop the "Global:" / "(iface):" label colon and the #server-name and
	// %interface decorations, leaving an address with an optional port.
	field = strings.TrimSuffix(field, ":")
	if i := strings.IndexByte(field, '#'); i >= 0 {
		field = field[:i]
	}
	if i := strings.IndexByte(field, '%'); i >= 0 {
		field = field[:i]
	}
	if ap, err := netip.ParseAddrPort(field); err == nil {
		return ap, true
	}
	if addr, err := netip.ParseAddr(strings.Trim(field, "[]")); err == nil {
		return netip.AddrPortFrom(addr, 53), true
	}
	return netip.AddrPort{}, false
}

func compareAddrPort(a, b netip.AddrPort) int {
	if c := a.Addr().Compare(b.Addr()); c != 0 {
		return c
	}
	return int(a.Port()) - int(b.Port())
}
