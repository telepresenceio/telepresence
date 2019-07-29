package main

import (
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"
)

// Connect the daemon to a cluster
func (d *Daemon) Connect(_ *http.Request, args *ConnectArgs, reply *StringReply) error {
	// Sanity checks
	if d.cluster != nil {
		reply.Message = "Already connected"
		return nil
	}
	if d.bridge != nil {
		reply.Message = "Not ready: Trying to disconnect"
		return nil
	}
	if !d.network.IsOkay() {
		reply.Message = "Not ready: Establishing network overrides"
		return nil
	}

	cluster, err := TrackKCluster(d.p, args)
	if err != nil {
		reply.Message = err.Error()
		return nil
	}
	d.cluster = cluster

	if err := d.FindTeleproxy(); err != nil {
		return err
	}
	bridge, err := CheckedRetryingCommand(
		d.p,
		"bridge",
		[]string{d.teleproxy, "--mode", "bridge"},
		args.RAI,
		checkBridge,
		10*time.Second,
	)
	if err != nil {
		reply.Message = err.Error()
		d.cluster.Close()
		d.cluster = nil
		return nil
	}
	d.bridge = bridge
	d.cluster.SetBridgeCheck(d.bridge.IsOkay)

	reply.Message = fmt.Sprintf(
		"Connected to context %s (%s)", d.cluster.Context(), d.cluster.Server(),
	)
	return nil
}

// Disconnect from the connected cluster
func (d *Daemon) Disconnect(_ *http.Request, _ *EmptyArgs, reply *StringReply) error {
	// Sanity checks
	if d.cluster == nil {
		reply.Message = "Not connected"
		return nil
	}

	if d.bridge != nil {
		_ = d.bridge.Close()
		d.bridge = nil
	}
	err := d.cluster.Close()
	d.cluster = nil

	reply.Message = "Disconnected"
	return err
}

// checkBridge checks the status of teleproxy bridge by doing the equivalent of
// curl -k https://kubernetes/api/. It's okay to create a new client each time
// because we don't want to reuse connections.
func checkBridge() error {
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
