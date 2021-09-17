package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agent"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agentinit"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager"
)

func doMain(fn func(ctx context.Context, args ...string) error, args ...string) {
	ctx := makeBaseLogger(context.Background())

	if err := fn(ctx, args...); err != nil {
		dlog.Errorf(ctx, "quit: %v", err)
		os.Exit(1)
	}
}

func main() {
	if len(os.Args) > 1 {
		switch name := os.Args[1]; name {
		case "agent":
			doMain(agent.Main, os.Args[2:]...)
		case "manager":
			doMain(manager.Main, os.Args[2:]...)
		case "agent-init":
			doMain(agentinit.Main, os.Args[2:]...)
		default:
			fmt.Println("traffic: unknown command:", name)
			os.Exit(127)
		}
		return
	}

	switch name := filepath.Base(os.Args[0]); name {
	case "traffic-agent":
		doMain(agent.Main, os.Args[1:]...)
	case "traffic-agent-init":
		doMain(agentinit.Main, os.Args[1:]...)
	case "traffic-manager":
		fallthrough
	default:
		doMain(manager.Main, os.Args[1:]...)
	}
}
