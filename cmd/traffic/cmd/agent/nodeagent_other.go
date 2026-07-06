//go:build !linux

package agent

import (
	"context"
	"errors"
)

// This file needs to exist because nodeagent_linux.go uses Linux-only /proc and CRI-socket
// APIs that don't build on other platforms. It really doesn't matter, since there's no
// support for non-Linux node-agent hosts, but it'll fail the build otherwise.

// NodeAgentMain is the entrypoint for the node-agent Job pod.
func NodeAgentMain(ctx context.Context, args ...string) error {
	return errors.New("node-agent is only supported on Linux")
}
