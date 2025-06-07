package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
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
		ok, _ = strconv.ParseBool(debug)
		logrus.SetLevel(logrus.DebugLevel)
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-sigs
		logrus.Info("Received shutdown signal")
		cancel()
	}()
	if err := network.NewHandler(driver.New(ctx)).ServeUnix(pluginSocket, 0); err != nil {
		logrus.Fatal(err)
	}
}
