package agentinit

import (
	"context"
	"fmt"
)

// This file needs to exist because agent_init.go imports iptables, which obviously doesn't exist on windows.
// It really doesn't matter, since there's no support for windows-based containers of any sort, but it'll fail the build.

// Main is the main function for the agent init container.
func Main(ctx context.Context, args ...string) error {
	return fmt.Errorf("windows-based init agent is not a thing")
}
