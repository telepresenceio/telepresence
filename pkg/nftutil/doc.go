//go:build linux

// Package nftutil provides the shared nftables core used by the native
// rulesets in pkg/agentnft (the traffic-agent, programmed into a pod's
// network namespace) and pkg/routenft (the cluster route-controller,
// programmed into the node's host network namespace).
//
// Ruleset pairs an nftables table with the chains, sets, and rules it
// contains; a package builds one out of plain Go values and passes it to
// Apply, which programs it into the kernel over netlink as a single atomic,
// idempotent full-replace batch. Teardown removes a named table for a given
// address family. SetData pairs a set or map with its elements so Apply can
// add both together. AddrLayout returns the network-header offset and
// length of the destination address field for a table family; IntervalSet
// and IntervalElements build the interval-set representation of a list of
// subnets.
//
// Everything here is a pure function or a plain data type built on top of
// github.com/google/nftables, except Apply and Teardown, which are the only
// functions in the package that open a netlink connection.
package nftutil
