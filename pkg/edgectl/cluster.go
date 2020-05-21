package edgectl

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/datawire/ambassador/pkg/k8s"
	"github.com/datawire/ambassador/pkg/supervisor"
)

var simpleTransport = &http.Transport{
	// #nosec G402
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	Proxy:           nil,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 1 * time.Second,
		DualStack: true,
	}).DialContext,
	DisableKeepAlives: true,
}

var hClient = &http.Client{
	Transport: simpleTransport,
	Timeout:   15 * time.Second,
}

// Connect the daemon to a cluster
func (d *Daemon) Connect(
	p *supervisor.Process, out *Emitter, rai *RunAsInfo,
	context, namespace, managerNs string, kargs []string,
	installID string, isCI bool,
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

	previewHost, err := getClusterPreviewHostname(p, cluster)
	if err != nil {
		p.Logf("get preview URL hostname: %+v", err)
		previewHost = ""
	}

	bridge, err := CheckedRetryingCommand(
		p,
		"bridge",
		[]string{GetExe(), "teleproxy", "bridge", cluster.context, cluster.namespace},
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

	tmgr, err := NewTrafficManager(p, d.cluster, managerNs, installID, isCI)
	if err != nil {
		out.Println()
		out.Println("Unable to connect to the traffic manager in your cluster.")
		out.Println("The intercept feature will not be available.")
		out.Println("Error was:", err)
		// out.Println("Use <some command> to set up the traffic manager.") // FIXME
		out.Send("intercept", false)
	} else {
		tmgr.previewHost = previewHost
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

// getClusterPreviewHostname returns the hostname of the first Host resource it
// finds that has Preview URLs enabled with a supported URL type.
func getClusterPreviewHostname(p *supervisor.Process, cluster *KCluster) (hostname string, err error) {
	p.Log("Looking for a Host with Preview URLs enabled")

	// kubectl get hosts, in all namespaces or in this namespace
	var outBytes []byte
	outBytes, err = func() ([]byte, error) {
		clusterCmd := cluster.GetKubectlCmdNoNamespace(p, "get", "host", "-o", "yaml", "--all-namespaces")
		if outBytes, err := clusterCmd.CombinedOutput(); err == nil {
			return outBytes, nil
		}

		nsCmd := cluster.GetKubectlCmd(p, "get", "host", "-o", "yaml")
		if outBytes, err := nsCmd.CombinedOutput(); err == nil {
			return outBytes, nil
		} else {
			return nil, err
		}
	}()
	if err != nil {
		return
	}

	// Parse the output
	hostLists, kerr := k8s.ParseResources("get hosts", string(outBytes))
	if kerr != nil {
		err = kerr
		return
	}
	if len(hostLists) != 1 {
		err = errors.Errorf("weird result with length %d", len(hostLists))
		return
	}

	// Grab the "items" slice, as the result should be a list of Host resources
	hostItems := k8s.Map(hostLists[0]).GetMaps("items")
	p.Logf("Found %d Host resources", len(hostItems))

	// Loop over Hosts looking for a Preview URL hostname
	for _, hostItem := range hostItems {
		host := k8s.Resource(hostItem)
		logEntry := fmt.Sprintf("- Host %s / %s: %%s", host.Namespace(), host.Name())

		previewUrlSpec := host.Spec().GetMap("previewUrl")
		if len(previewUrlSpec) == 0 {
			p.Logf(logEntry, "no preview URL config")
			continue
		}

		if enabled, ok := previewUrlSpec["enabled"].(bool); !ok || !enabled {
			p.Logf(logEntry, "preview URL not enabled")
			continue
		}

		if pType, ok := previewUrlSpec["type"].(string); !ok || pType != "Path" {
			p.Logf(logEntry+": %#v", "unsupported preview URL type", previewUrlSpec["type"])
			continue
		}

		if hostname = host.Spec().GetString("hostname"); hostname == "" {
			p.Logf(logEntry, "empty hostname???")
			continue
		}

		p.Logf(logEntry+": %q", "SUCCESS! Hostname is", hostname)
		return
	}

	p.Logf("No appropriate Host resource found.")
	return
}

// checkBridge checks the status of teleproxy bridge by doing the equivalent of
//  curl http://traffic-proxy.svc.cluster.local:8022.
// Note there is no namespace specified, as we are checking for bridge status in the
// current namespace. We only care about establishing a connection, not the response.
func checkBridge(p *supervisor.Process) error {
	address := "traffic-proxy.svc.cluster.local:8022"
	conn, err := net.DialTimeout("tcp", address, 15*time.Second)
	if err != nil {
		return errors.Wrap(err, "tcp connect")
	}
	if conn != nil {
		defer conn.Close()
	} else {
		return fmt.Errorf("fail to establish tcp connection to %v", address)
	}
	return nil
}

// TrafficManager is a handle to access the Traffic Manager in a
// cluster.
type TrafficManager struct {
	crc            Resource
	apiPort        int
	sshPort        int
	namespace      string
	interceptables []string
	totalClusCepts int
	snapshotSent   bool
	installID      string // edgectl's install ID
	connectCI      bool   // whether --ci was passed to connect
	apiErr         error  // holds the latest traffic-manager API error
	licenseInfo    string // license information from traffic-manager
	previewHost    string // hostname to use for preview URLs, if enabled
}

// NewTrafficManager returns a TrafficManager resource for the given
// cluster if it has a Traffic Manager service.
func NewTrafficManager(p *supervisor.Process, cluster *KCluster, managerNs string, installID string, isCI bool) (*TrafficManager, error) {
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
		installID: installID,
		connectCI: isCI,
	}

	pf, err := CheckedRetryingCommand(p, "traffic-kpf", kpfArgs, cluster.RAI(), tm.check, 15*time.Second)
	if err != nil {
		return nil, err
	}
	tm.crc = pf
	return tm, nil
}

func (tm *TrafficManager) check(p *supervisor.Process) error {
	body, code, err := tm.request("GET", "state", []byte{})
	if err != nil {
		return err
	}

	if code != http.StatusOK {
		tm.apiErr = fmt.Errorf("%v: %v", code, body)
		return tm.apiErr
	}
	tm.apiErr = nil

	var state map[string]interface{}
	if err := json.Unmarshal([]byte(body), &state); err != nil {
		p.Logf("check: bad JSON from tm: %v", err)
		p.Logf("check: JSON data is: %q", body)
		return err
	}
	if licenseInfo, ok := state["LicenseInfo"]; ok {
		tm.licenseInfo = licenseInfo.(string)
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
		resp, err := hClient.Post("http://teleproxy/api/tables/", "application/json", strings.NewReader(body))
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

	req.Header.Set("edgectl-install-id", tm.installID)
	req.Header.Set("edgectl-connect-ci", strconv.FormatBool(tm.connectCI))

	resp, err := hClient.Do(req)
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
