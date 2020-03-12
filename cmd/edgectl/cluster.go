package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/datawire/ambassador/pkg/supervisor"
)

// Connect the daemon to a cluster
func (d *Daemon) Connect(
	p *supervisor.Process, out *Emitter, rai *RunAsInfo,
	context, namespace, managerNs string, kargs []string,
) error {
	// Sanity checks
	if d.cluster != nil {
		out.Println("Already connected")
		out.Send("connect", "Already connected")
		return nil
	}
	if d.bridge != nil {
		out.Println("Not ready: Trying to disconnect")
		out.Send("connect", "Not ready: Trying to disconnect")
		return nil
	}
	if d.network == nil {
		out.Println("Not ready: Network overrides are paused (use \"edgectl resume\")")
		out.Send("connect", "Not ready: Paused")
		return nil
	}
	if !d.network.IsOkay() {
		out.Println("Not ready: Establishing network overrides")
		out.Send("connect", "Not ready: Establishing network overrides")
		return nil
	}

	out.Printf("Connecting to traffic manager in namespace %s...\n", managerNs)
	out.Send("connect", "Connecting...")
	cluster, err := TrackKCluster(p, rai, context, namespace, kargs)
	if err != nil {
		out.Println(err.Error())
		out.Send("failed", err.Error())
		out.SendExit(1)
		return nil
	}
	d.cluster = cluster

	bridge, err := CheckedRetryingCommand(
		p,
		"bridge",
		[]string{edgectl, "teleproxy", "bridge", cluster.context, cluster.namespace},
		rai,
		checkBridge,
		15*time.Second,
	)
	if err != nil {
		out.Println(err.Error())
		out.Send("failed", err.Error())
		out.SendExit(1)
		d.cluster.Close()
		d.cluster = nil
		return nil
	}
	d.bridge = bridge
	d.cluster.SetBridgeCheck(d.bridge.IsOkay)

	out.Printf(
		"Connected to context %s (%s)\n", d.cluster.Context(), d.cluster.Server(),
	)
	out.Send("cluster.context", d.cluster.Context())
	out.Send("cluster.server", d.cluster.Server())

	tmgr, err := NewTrafficManager(p, d.cluster, managerNs)
	if err != nil {
		out.Println()
		out.Println("Unable to connect to the traffic manager in your cluster.")
		out.Println("The intercept feature will not be available.")
		out.Println("Error was:", err)
		// out.Println("Use <some command> to set up the traffic manager.") // FIXME
		out.Send("intercept", false)
	} else {
		d.trafficMgr = tmgr
		out.Send("intercept", true)
	}
	return nil
}

// Disconnect from the connected cluster
func (d *Daemon) Disconnect(p *supervisor.Process, out *Emitter) error {
	// Sanity checks
	if d.cluster == nil {
		out.Println("Not connected (use 'edgectl connect' to connect to your cluster)")
		out.Send("disconnect", "Not connected")
		return nil
	}

	_ = d.ClearIntercepts(p)
	if d.bridge != nil {
		d.cluster.SetBridgeCheck(nil) // Stop depending on this bridge
		_ = d.bridge.Close()
		d.bridge = nil
	}
	if d.trafficMgr != nil {
		_ = d.trafficMgr.Close()
		d.trafficMgr = nil
	}
	err := d.cluster.Close()
	d.cluster = nil

	out.Println("Disconnected")
	out.Send("disconnect", "Disconnected")
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
	client := http.Client{Timeout: 10 * time.Second, Transport: tr}
	res, err := client.Get("https://kubernetes.default/api/")
	if err != nil {
		return errors.Wrap(err, "get")
	}
	_, err = ioutil.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return errors.Wrap(err, "read body")
	}
	return nil
}

// TrafficManager is a handle to access the Traffic Manager in a
// cluster.
type TrafficManager struct {
	crc            Resource
	apiPort        int
	sshPort        int
	client         *http.Client
	namespace      string
	interceptables []string
	totalClusCepts int
	snapshotSent   bool
}

// NewTrafficManager returns a TrafficManager resource for the given
// cluster if it has a Traffic Manager service.
func NewTrafficManager(p *supervisor.Process, cluster *KCluster, managerNs string) (*TrafficManager, error) {
	cmd := cluster.GetKubectlCmd(p, "get", "-n", managerNs, "svc/telepresence-proxy", "deploy/telepresence-proxy")
	err := cmd.Run()
	if err != nil {
		return nil, errors.Wrap(err, "kubectl get svc/deploy telepresency-proxy")
	}

	apiPort, err := GetFreePort()
	if err != nil {
		return nil, errors.Wrap(err, "get free port for API")
	}
	sshPort, err := GetFreePort()
	if err != nil {
		return nil, errors.Wrap(err, "get free port for ssh")
	}
	kpfArgStr := fmt.Sprintf("port-forward -n %s svc/telepresence-proxy %d:8022 %d:8081", managerNs, sshPort, apiPort)
	kpfArgs := cluster.GetKubectlArgs(strings.Fields(kpfArgStr)...)
	tm := &TrafficManager{
		apiPort:   apiPort,
		sshPort:   sshPort,
		namespace: managerNs,
		client:    &http.Client{Timeout: 10 * time.Second},
	}

	pf, err := CheckedRetryingCommand(p, "traffic-kpf", kpfArgs, cluster.RAI(), tm.check, 15*time.Second)
	if err != nil {
		return nil, err
	}
	tm.crc = pf
	return tm, nil
}

func (tm *TrafficManager) check(p *supervisor.Process) error {
	body, _, err := tm.request("GET", "state", []byte{})
	if err != nil {
		return err
	}
	var state map[string]interface{}
	if err := json.Unmarshal([]byte(body), &state); err != nil {
		p.Logf("check: bad JSON from tm: %v", err)
		p.Logf("check: JSON data is: %q", body)
		return err
	}
	deployments, ok := state["Deployments"].(map[string]interface{})
	if !ok {
		p.Log("check: failed to get deployment info")
		p.Logf("check: JSON data is: %q", body)
	}
	tm.interceptables = make([]string, len(deployments))
	tm.totalClusCepts = 0
	idx := 0
	for deployment := range deployments {
		tm.interceptables[idx] = deployment
		idx++
		info, ok := deployments[deployment].(map[string]interface{})
		if !ok {
			continue
		}
		cepts, ok := info["Intercepts"].([]interface{})
		if ok {
			tm.totalClusCepts += len(cepts)
		}
	}

	if !tm.snapshotSent {
		p.Log("trying to send snapshot")
		tm.snapshotSent = true // don't try again, even if this fails
		body, code, err := tm.request("GET", "snapshot", []byte{})
		if err != nil || code != 200 {
			p.Logf("snapshot request failed: %v", err)
			return nil
		}
		resp, err := tm.client.Post("http://teleproxy/api/tables/", "application/json", strings.NewReader(body))
		if err != nil {
			p.Logf("snapshot post failed: %v", err)
			return nil
		}
		_, _ = ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		p.Log("snapshot sent!")
	}

	return nil
}

func (tm *TrafficManager) request(method, path string, data []byte) (result string, code int, err error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/%s", tm.apiPort, path)
	req, err := http.NewRequest(method, url, bytes.NewBuffer(data))
	if err != nil {
		return
	}
	resp, err := tm.client.Do(req)
	if err != nil {
		err = errors.Wrap(err, "get")
		return
	}
	defer resp.Body.Close()
	code = resp.StatusCode
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		err = errors.Wrap(err, "read body")
		return
	}
	result = string(body)
	return
}

// Name implements Resource
func (tm *TrafficManager) Name() string {
	return "trafficMgr"
}

// IsOkay implements Resource
func (tm *TrafficManager) IsOkay() bool {
	return tm.crc.IsOkay()
}

// Close implements Resource
func (tm *TrafficManager) Close() error {
	return tm.crc.Close()
}
