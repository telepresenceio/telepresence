//go:build linux

// Package netns runs functions and opens listeners inside the network
// namespace of another process. It is used by the node-hosted traffic-agent
// so its forwarders can bind intercept ports in a target pod's network
// namespace without moving the whole agent process into that namespace.
//
// It is built on top of github.com/vishvananda/netns, which provides the
// low-level primitives (getting a handle to a namespace, and moving the
// calling thread into one).
//
// Linux-only: network namespaces are a Linux kernel feature. Entering a
// namespace other than the calling process's own requires the process to
// have CAP_SYS_ADMIN. All operations in this package pin an OS thread for
// their duration; see the Do function for details.
package netns
