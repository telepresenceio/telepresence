package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/pkg/errors"

	"github.com/datawire/ambassador/pkg/supervisor"
)

// MakeNetOverride sets up the network override resource for the daemon
func (d *Daemon) MakeNetOverride(p *supervisor.Process) error {
	netOverride, err := CheckedRetryingCommand(
		p,
		"netOverride",
		[]string{edgectl, "teleproxy", "intercept", d.dns, d.fallback},
		&RunAsInfo{},
		checkNetOverride,
		10*time.Second,
	)
	if err != nil {
		return errors.Wrap(err, "teleproxy initial launch")
	}
	d.network = netOverride
	return nil
}

// checkNetOverride checks the status of teleproxy intercept by doing the
// equivalent of curl http://teleproxy/api/tables/. It's okay to create a new
// client each time because we don't want to reuse connections.
func checkNetOverride(p *supervisor.Process) error {
	client := http.Client{Timeout: 10 * time.Second}
	res, err := client.Get(fmt.Sprintf(
		"http://teleproxy%d.cachebust.telepresence.io/api/tables",
		time.Now().Unix(),
	))
	if err != nil {
		return err
	}
	_, err = ioutil.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return err
	}
	return nil
}
