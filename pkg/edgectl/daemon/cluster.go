package daemon

import (
	"bufio"
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

	"github.com/datawire/ambassador/internal/pkg/edgectl"
	"github.com/datawire/ambassador/pkg/api/edgectl/rpc"
	"github.com/datawire/ambassador/pkg/k8s"
	"github.com/datawire/ambassador/pkg/metriton"
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

// connect the daemon to a cluster
func (d *daemon) connect(p *supervisor.Process, cr *rpc.ConnectRequest) *rpc.ConnectResponse {
	reporter := &metriton.Reporter{
		Application:  "edgectl",
		Version:      edgectl.Version,
		GetInstallID: func(_ *metriton.Reporter) (string, error) { return cr.InstallID, nil },
		BaseMetadata: map[string]interface{}{"mode": "daemon"},
	}

	if _, err := reporter.Report(p.Context(), map[string]interface{}{"action": "connect"}); err != nil {
		p.Logf("report failed: %+v", err)
	}

	// Sanity checks
	r := &rpc.ConnectResponse{}
	if d.cluster != nil {
		r.Error = rpc.ConnectResponse_AlreadyConnected
		return r
	}
	if d.bridge != nil {
		r.Error = rpc.ConnectResponse_Disconnecting
		return r
	}
	if d.network == nil {
		r.Error = rpc.ConnectResponse_Paused
		return r
	}
	nwOk := d.network.IsOkay()
	if !nwOk {
		if cr.WaitForNetwork > 0 {
			// wait the given number of seconds, checking every fifth of a second.
			for i := 5 * cr.WaitForNetwork; !nwOk && i > 0; i-- {
				time.Sleep(200 * time.Millisecond)
				nwOk = d.network.IsOkay()
			}
		}
		if !nwOk {
			r.Error = rpc.ConnectResponse_EstablishingOverrides
			return r
		}
	}

	p.Logf("Connecting to traffic manager in namespace %s...", cr.ManagerNS)
	cluster, err := TrackKCluster(p, runAsUserFromRPC(cr.User), cr.Context, cr.Namespace, cr.Args)
	if err != nil {
		r.Error = rpc.ConnectResponse_ClusterFailed
		r.ErrorText = err.Error()
		return r
	}
	d.cluster = cluster

	previewHost, err := getClusterPreviewHostname(p, cluster)
	if err != nil {
		p.Logf("get preview URL hostname: %+v", err)
		previewHost = ""
	}

	rai := runAsUserFromRPC(cr.User)
	bridge, err := CheckedRetryingCommand(
		p,
		"bridge",
		[]string{edgectl.GetExe(), "teleproxy", "bridge", cluster.context, cluster.namespace},
		rai,
		checkBridge,
		15*time.Second,
	)
	if err != nil {
		d.cluster.Close()
		d.cluster = nil
		r.Error = rpc.ConnectResponse_BridgeFailed
		r.ErrorText = err.Error()
		return r
	}
	d.bridge = bridge
	d.cluster.SetBridgeCheck(d.bridge.IsOkay)
	p.Logf("Connected to context %s (%s)", d.cluster.Context(), d.cluster.Server())

	r.ClusterContext = d.cluster.Context()
	r.ClusterServer = d.cluster.Server()

	tmgr, err := NewTrafficManager(p, d.cluster, cr.ManagerNS, cr.InstallID, cr.IsCI)
	if err != nil {
		p.Logf("Unable to connect to TrafficManager: %s", err)
		r.Error = rpc.ConnectResponse_TrafficManagerFailed
		r.ErrorText = err.Error()
		return r
	}
	tmgr.previewHost = previewHost
	d.trafficMgr = tmgr
	return r
}

// disconnect from the connected cluster
func (d *daemon) disconnect(p *supervisor.Process) *rpc.DisconnectResponse {
	// Sanity checks
	r := &rpc.DisconnectResponse{}
	if d.cluster == nil {
		r.Error = rpc.DisconnectResponse_NotConnected
		return r
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
	if err != nil {
		r.Error = rpc.DisconnectResponse_DisconnectFailed
		r.ErrorText = err.Error()
	}
	return r
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

		// missing type, default is "Path" --> success
		// type is present, set to "Path" --> success
		// otherwise --> failure
		if pType, ok := previewUrlSpec["type"].(string); ok && pType != "Path" {
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
//  curl http://traffic-proxy.svc:8022.
// Note there is no namespace specified, as we are checking for bridge status in the
// current namespace.
func checkBridge(_ *supervisor.Process) error {
	address := "traffic-proxy.svc:8022"
	conn, err := net.DialTimeout("tcp", address, 15*time.Second)
	if err != nil {
		return errors.Wrap(err, "tcp connect")
	}
	if conn != nil {
		defer conn.Close()
		msg, _, err := bufio.NewReader(conn).ReadLine()
		if err != nil {
			return errors.Wrap(err, "tcp read")
		}
		if !strings.Contains(string(msg), "SSH") {
			return fmt.Errorf("expected SSH prompt, got: %v", string(msg))
		}
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
