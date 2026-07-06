//go:build linux

package nftutil

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/nftables"
	"golang.org/x/sys/unix"

	"github.com/telepresenceio/clog"
)

// Apply programs rs into the kernel as a full, idempotent, atomic replace in a
// single netlink batch: nft batches are all-or-nothing, so if any message is
// rejected none of it takes effect.
//
// Idempotency is achieved by (add, delete, re-add) of the table at the head of
// the batch. AddTable uses NLM_F_CREATE (no error if it already exists), which
// guarantees the following DelTable -- which would otherwise fail with ENOENT
// on a first run -- always has a table to remove. DelTable then discards any
// prior chains/sets/rules, and the second AddTable recreates the table empty
// before its fresh contents are added. This is required because AddRule is not
// idempotent (google/nftables adds rules with NLM_F_CREATE only, so reapplying
// on a live table would duplicate them).
//
// opts configures the underlying netlink connection -- most notably
// nftables.WithNetNSFd to target a network namespace other than the caller's
// current one; with no opts, Apply programs the caller's current namespace.
func Apply(ctx context.Context, rs *Ruleset, opts ...nftables.ConnOption) error {
	conn, err := nftables.New(opts...)
	if err != nil {
		return fmt.Errorf("nftables: unable to open netlink connection: %w", err)
	}

	// Full-replace preamble: ensure-exists, delete, recreate.
	conn.AddTable(rs.Table)
	conn.DelTable(rs.Table)
	conn.AddTable(rs.Table)

	for _, c := range rs.Chains {
		conn.AddChain(c)
	}
	for _, s := range rs.Sets {
		if err := conn.AddSet(s.Set, s.Elements); err != nil {
			return fmt.Errorf("nftables: unable to add set %q to table %q: %w", s.Set.Name, rs.Table.Name, err)
		}
	}
	for _, r := range rs.Rules {
		conn.AddRule(r)
	}

	if err := conn.Flush(); err != nil {
		return fmt.Errorf("nftables: unable to apply table %q: %w", rs.Table.Name, err)
	}
	clog.Debugf(ctx, "nftables: applied table %q (family %d) with %d rule(s)", rs.Table.Name, rs.Table.Family, len(rs.Rules))
	return nil
}

// Teardown removes the named table for family, deleting every chain, set, and
// rule it contains. It is not an error for the table to already be absent, so
// Teardown is safe to call unconditionally (e.g. from a container exiting
// after a failed or partial Apply).
func Teardown(ctx context.Context, name string, family nftables.TableFamily, opts ...nftables.ConnOption) error {
	conn, err := nftables.New(opts...)
	if err != nil {
		return fmt.Errorf("nftables: unable to open netlink connection: %w", err)
	}
	conn.DelTable(&nftables.Table{Name: name, Family: family})
	if err := conn.Flush(); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("nftables: unable to tear down table %q: %w", name, err)
	}
	clog.Debugf(ctx, "nftables: removed table %q (family %d)", name, family)
	return nil
}
