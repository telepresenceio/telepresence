package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/docker/go-plugins-helpers/network"
	"github.com/sirupsen/logrus"

	"github.com/telepresenceio/telepresence/cmd/teleroute/driver"
	"github.com/telepresenceio/telepresence/cmd/teleroute/log"
)

const pluginSocket = "/run/docker/plugins/teleroute.sock"
const pluginLog = "/var/log/teleroute.log"

func main() {
	logrus.SetFormatter(log.NewFormatter(time.TimeOnly))
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
	if err := network.NewHandler(driver.New()).ServeUnix(pluginSocket, 0); err != nil {
		logrus.Fatal(err)
	}
}
