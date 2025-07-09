package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/docker/go-plugins-helpers/network"
	"github.com/sirupsen/logrus"

	"github.com/telepresenceio/telepresence/cmd/teleroute/driver"
	"github.com/telepresenceio/telepresence/cmd/teleroute/log"
)

const pluginSocket = "/run/docker/plugins/teleroute.sock"
const pluginLog = "/var/log/teleroute.log"

func main() {
	logrus.SetFormatter(log.NewFormatter("15:04:05.0000"))
	lf, err := os.Create(pluginLog)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Failed to create plugin log file %s: %s\n", pluginLog, err)
		os.Exit(1)
	}
	logrus.SetOutput(lf)
	if debug, ok := os.LookupEnv("DEBUG"); ok {
		if ok, _ = strconv.ParseBool(debug); ok {
			logrus.SetLevel(logrus.DebugLevel)
		}
	}
	pid, err := getPluginHostPID()
	if err != nil {
		logrus.Fatal(err)
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-sigs
		logrus.Info("Received shutdown signal")
		cancel()
	}()
	if err := network.NewHandler(driver.New(ctx, pid)).ServeUnix(pluginSocket, 0); err != nil {
		logrus.Fatal(err)
	}
}

const pluginEntryPoint = "/bin/docker-network-teleroute"

func getPluginHostPID() (int, error) {
	pls, err := filepath.Glob("/proc/*/exe")
	if err != nil {
		return 0, fmt.Errorf("error listing entries matching glob /proc/*/exe: %v", err)
	}
	for _, pl := range pls {
		lt, _ := os.Readlink(pl)
		if lt == pluginEntryPoint {
			logrus.Debugf("%s links to %s", pl, pluginEntryPoint)
			if pid, err := strconv.Atoi(pl[6 : len(pl)-4]); err == nil {
				logrus.Debugf("pid of %s is %d", pluginEntryPoint, pid)
				return pid, nil
			}
		}
	}
	return 0, fmt.Errorf("unable to find PID for %s", pluginEntryPoint)
}
