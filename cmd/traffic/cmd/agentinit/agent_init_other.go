//go:build !linux

package agentinit

import (
	"context"
	"fmt"
)

// This file needs to exist because agent_init.go uses Linux-only netfilter/netlink APIs.
// It really doesn't matter, since containers are always Linux-based, but cmd/traffic must compile on every platform.

// Main is the main function for the agent init container.
func Main(ctx context.Context, args ...string) error {
	return fmt.Errorf("the init agent only runs on linux")
}
