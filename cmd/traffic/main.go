package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agent"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agentinit"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

func main() {
	cmds := map[string]func(ctx context.Context, args ...string) error{
		"agent":      agent.Main,
		"agent-init": agentinit.Main,
		"manager":    manager.Main,
	}

	var name string
	var args []string
	if len(os.Args) > 1 {
		name = os.Args[1]
		args = os.Args[2:]
	} else {
		argv0 := filepath.Base(os.Args[0])
		name = strings.TrimPrefix(argv0, "traffic-")
		args = os.Args[1:]
		if _, ok := cmds[name]; !ok || !strings.HasPrefix(argv0, "traffic-") {
			name = "manager"
		}
	}

	if cmd, cmdOK := cmds[name]; cmdOK {
		ctx := context.Background()
		lvl := os.Getenv("LOG_LEVEL")
		if strings.EqualFold(lvl, "warning") {
			lvl = "warn"
		}
		ctx = log.MakeBaseLogger(ctx, os.Stdout, lvl)
		if err := cmd(ctx, args...); err != nil {
			clog.Errorf(ctx, "quit: %v", err)
			os.Exit(1)
		}
	} else {
		_, _ = fmt.Fprintln(os.Stderr, "traffic: unknown command:", name)
		os.Exit(127)
	}
}
