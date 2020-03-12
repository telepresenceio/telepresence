package main

import (
	"fmt"
	"io/ioutil"
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
// equivalent of curl http://teleproxy/api/tables/.
func checkNetOverride(p *supervisor.Process) error {
	res, err := hClient.Get(fmt.Sprintf(
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
