//go:build linux

package nftutil

import (
	"github.com/google/nftables"
	"github.com/google/nftables/expr"
)

// Ruleset is the netlink-agnostic representation of one nftables table: the
// table itself plus the chains, sets/maps (with their elements), and rules it
// contains. Package-specific constructors build it out of plain Go values;
// Apply programs it into the kernel. Keeping construction separate from
// application is what makes rulesets unit-testable without root or a network
// namespace.
type Ruleset struct {
	Table  *nftables.Table
	Chains []*nftables.Chain
	Sets   []*SetData
	Rules  []*nftables.Rule
}

// AddRule appends a rule holding exprs to chain.
func (rs *Ruleset) AddRule(chain *nftables.Chain, exprs []expr.Any) {
	rs.Rules = append(rs.Rules, &nftables.Rule{Table: rs.Table, Chain: chain, Exprs: exprs})
}
