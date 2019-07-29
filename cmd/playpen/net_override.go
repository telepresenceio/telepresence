package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/pkg/errors"
)

// FindTeleproxy finds a compatible version of Teleproxy in your PATH
func (d *Daemon) FindTeleproxy() error {
	if len(d.teleproxy) == 0 {
		path, err := exec.LookPath("teleproxy")
		if err != nil {
			return err
		}
		cmd := exec.Command(path, "--version")
		outputBytes, err := cmd.CombinedOutput()
		if err != nil {
			return errors.Wrap(err, "teleproxy --version")
		}
		output := string(outputBytes)
		if !strings.Contains(output, "version 0.6") {
			return fmt.Errorf(
				"required teleproxy 0.6.x not found; found %s in your PATH",
				output,
			)
		}
		d.teleproxy = path
	}
	return nil
}

func (d *Daemon) MakeNetOverride(p *supervisor.Process) error {
	if err := d.FindTeleproxy(); err != nil {
		return err
	}
	netOverride, err := CheckedRetryingCommand(
		p,
		"netOverride",
		[]string{d.teleproxy, "--mode", "intercept"},
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
func checkNetOverride() error {
	client := http.Client{Timeout: 3 * time.Second}
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
