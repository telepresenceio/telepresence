package main

import (
	"os"
	"strconv"

	"github.com/docker/go-plugins-helpers/network"
	log "github.com/sirupsen/logrus"

	"github.com/telepresenceio/telepresence/cmd/teleroute/driver"
)

const pluginSocket = "/run/docker/plugins/teleroute.sock"

func main() {
	if debug, ok := os.LookupEnv("DEBUG"); ok {
		ok, _ = strconv.ParseBool(debug)
		log.SetLevel(log.DebugLevel)
	}
	if err := network.NewHandler(driver.New()).ServeUnix(pluginSocket, 0); err != nil {
		log.Fatal(err)
	}
}
