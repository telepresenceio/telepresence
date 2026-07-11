//go:build linux

// Package cri queries a container runtime over its CRI (Container Runtime
// Interface) unix socket to resolve a Kubernetes container ID to the host
// PID of the container's init process. It is used by the node-hosted
// traffic-agent to locate the process of an unmodified target container
// running elsewhere on the same node.
package cri
