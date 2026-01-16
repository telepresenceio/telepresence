package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/docker/go-plugins-helpers/network"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/clog/handler"
	"github.com/telepresenceio/telepresence/cmd/teleroute/driver"
)

const (
	pluginSocket = "/run/docker/plugins/teleroute.sock"
	pluginLog    = "/var/log/teleroute.log"
)

func main() {
	lf, err := os.Create(pluginLog)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Failed to create plugin log file %s: %s\n", pluginLog, err)
		os.Exit(1)
	}
	defer lf.Close()

	level := clog.LevelInfo
	if debug, ok := os.LookupEnv("DEBUG"); ok {
		if ok, _ = strconv.ParseBool(debug); ok {
			level = clog.LevelDebug
		}
	}
	ctx := clog.WithLogger(context.Background(), slog.New(handler.NewText(handler.Output(lf), handler.TimeFormat("15:04:05.0000"), handler.EnabledLevel(level))))
	pid, err := getPluginHostPID(ctx)
	if err != nil {
		clog.Error(ctx, err)
		os.Exit(1)
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		<-sigs
		clog.Info(ctx, "Received shutdown signal")
		cancel()
	}()
	if err := network.NewHandler(driver.New(ctx, pid)).ServeUnix(pluginSocket, 0); err != nil {
		clog.Error(ctx, err)
		os.Exit(1)
	}
}

const pluginEntryPoint = "/bin/docker-network-teleroute"

func getPluginHostPID(ctx context.Context) (int, error) {
	pls, err := filepath.Glob("/proc/*/exe")
	if err != nil {
		return 0, fmt.Errorf("error listing entries matching glob /proc/*/exe: %v", err)
	}
	for _, pl := range pls {
		lt, _ := os.Readlink(pl)
		if lt == pluginEntryPoint {
			clog.Debugf(ctx, "%s links to %s", pl, pluginEntryPoint)
			if pid, err := strconv.Atoi(pl[6 : len(pl)-4]); err == nil {
				clog.Debugf(ctx, "pid of %s is %d", pluginEntryPoint, pid)
				return pid, nil
			}
		}
	}
	return 0, fmt.Errorf("unable to find PID for %s", pluginEntryPoint)
}
