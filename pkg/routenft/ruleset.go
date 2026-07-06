//go:build linux

package routenft

import (
	"errors"
	"fmt"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"

	"github.com/telepresenceio/telepresence/v2/pkg/nftutil"
)

// Ruleset is the fully constructed, netlink-agnostic representation of the
// route-controller's nftables ruleset for one address family. Build produces
// one from a Config; nftutil.Apply programs it into the kernel over netlink.
// As with pkg/agentnft, keeping construction separate from application is
// what makes the ruleset unit-testable without root: every field here is a
// plain Go value from the google/nftables package, never a live netlink
// handle.
type Ruleset struct {
	nftutil.Ruleset

	// Forward is the forward-hook filter chain that carries the single drop
	// rule. It runs at the standard filter priority: ordering against other
	// tools' FORWARD-hook chains does not matter here, because kube-proxy's
	// DNAT for active services happens earlier, in the prerouting nat hook,
	// so by the time any forward-hook chain sees the packet its destination
	// has already been rewritten for active services.
	Forward *nftables.Chain

	// ServiceCIDRs is the interval set of blackholed service CIDRs.
	ServiceCIDRs *nftutil.SetData
}

// Build constructs the ruleset described by cfg. It does not touch netlink.
func Build(cfg Config) (*Ruleset, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}

	table := &nftables.Table{Name: TableName, Family: cfg.Family}
	rs := &Ruleset{
		Ruleset: nftutil.Ruleset{Table: table},
		Forward: &nftables.Chain{
			Name:     chainForward,
			Table:    table,
			Type:     nftables.ChainTypeFilter,
			Hooknum:  nftables.ChainHookForward,
			Priority: nftables.ChainPriorityFilter,
		},
	}
	rs.Chains = append(rs.Chains, rs.Forward)

	var err error
	rs.ServiceCIDRs, err = nftutil.IntervalSet(table, setServiceCIDRs, cfg.CIDRs)
	if err != nil {
		return nil, fmt.Errorf("routenft: %w", err)
	}
	rs.Sets = append(rs.Sets, rs.ServiceCIDRs)

	off, ln := nftutil.AddrLayout(cfg.Family)
	rs.AddRule(rs.Forward, matchDaddrInSetDrop(off, ln, rs.ServiceCIDRs.Set))

	return rs, nil
}

// matchDaddrInSetDrop returns expressions for `ip daddr @set drop` (or the
// ip6 equivalent): drop any packet whose destination address is a member of
// set.
func matchDaddrInSetDrop(off, ln uint32, set *nftables.Set) []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: off, Len: ln},
		&expr.Lookup{SourceRegister: 1, SetName: set.Name, SetID: set.ID},
		&expr.Verdict{Kind: expr.VerdictDrop},
	}
}

func validate(cfg Config) error {
	if cfg.Family != nftables.TableFamilyIPv4 && cfg.Family != nftables.TableFamilyIPv6 {
		return fmt.Errorf("routenft: unsupported table family %v", cfg.Family)
	}
	if len(cfg.CIDRs) == 0 {
		return errors.New("routenft: at least one CIDR is required")
	}
	return nil
}
