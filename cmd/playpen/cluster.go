package main

import (
	"crypto/tls"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/datawire/teleproxy/pkg/supervisor"
)

// Connect the daemon to a cluster
func (d *Daemon) Connect(p *supervisor.Process, out *Emitter, rai *RunAsInfo, kargs []string) error {
	// Sanity checks
	if d.cluster != nil {
		out.Println("Already connected")
		return nil
	}
	if d.bridge != nil {
		out.Println("Not ready: Trying to disconnect")
		return nil
	}
	if !d.network.IsOkay() {
		out.Println("Not ready: Establishing network overrides")
		return nil
	}

	out.Println("Connecting...")
	cluster, err := TrackKCluster(p, rai, kargs)
	if err != nil {
		out.Println(err.Error())
		out.SendExit(1)
		return nil
	}
	d.cluster = cluster

	if err := d.FindTeleproxy(); err != nil {
		return err
	}
	bridge, err := CheckedRetryingCommand(
		p,
		"bridge",
		[]string{d.teleproxy, "--mode", "bridge"},
		rai,
		checkBridge,
		15*time.Second,
	)
	if err != nil {
		out.Println(err.Error())
		out.SendExit(1)
		d.cluster.Close()
		d.cluster = nil
		return nil
	}
	d.bridge = bridge
	d.cluster.SetBridgeCheck(d.bridge.IsOkay)

	out.Printf(
		"Connected to context %s (%s)", d.cluster.Context(), d.cluster.Server(),
	)
	return nil
}

// Disconnect from the connected cluster
func (d *Daemon) Disconnect(p *supervisor.Process, out *Emitter) error {
	// Sanity checks
	if d.cluster == nil {
		out.Println("Not connected")
		return nil
	}

	if d.bridge != nil {
		_ = d.bridge.Close()
		d.bridge = nil
	}
	err := d.cluster.Close()
	d.cluster = nil

	out.Println("Disconnected")
	return err
}

// checkBridge checks the status of teleproxy bridge by doing the equivalent of
// curl -k https://kubernetes/api/. It's okay to create a new client each time
// because we don't want to reuse connections.
func checkBridge(p *supervisor.Process) error {
	// A zero-value transport is (probably) okay because we set a tight overall
	// timeout on the client
	tr := &http.Transport{
		// #nosec G402
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := http.Client{Timeout: 3 * time.Second, Transport: tr}
	res, err := client.Get("https://kubernetes.default/api/")
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
