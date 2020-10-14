package connector

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

	"github.com/datawire/ambassador/internal/pkg/edgectl"
	"github.com/datawire/ambassador/pkg/supervisor"
)

// trafficManager is a handle to access the Traffic Manager in a
// cluster.
type trafficManager struct {
	crc            edgectl.Resource
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
	hClient        *http.Client
}

// newTrafficManager returns a TrafficManager resource for the given
// cluster if it has a Traffic Manager service.
func newTrafficManager(p *supervisor.Process, cluster *k8sCluster, managerNs string, installID string, isCI bool) (*trafficManager, error) {
	cmd := cluster.getKubectlCmd(p, "get", "-n", managerNs, "svc/telepresence-proxy", "deploy/telepresence-proxy")
	err := cmd.Run()
	if err != nil {
		return nil, errors.Wrap(err, "kubectl get svc/deploy telepresency-proxy")
	}

	apiPort, err := getFreePort()
	if err != nil {
		return nil, errors.Wrap(err, "get free port for API")
	}
	sshPort, err := getFreePort()
	if err != nil {
		return nil, errors.Wrap(err, "get free port for ssh")
	}
	kpfArgStr := fmt.Sprintf("port-forward -n %s svc/telepresence-proxy %d:8022 %d:8081", managerNs, sshPort, apiPort)
	kpfArgs := cluster.getKubectlArgs(strings.Fields(kpfArgStr)...)
	tm := &trafficManager{
		apiPort:   apiPort,
		sshPort:   sshPort,
		namespace: managerNs,
		installID: installID,
		connectCI: isCI,
		hClient: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				// #nosec G402
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
				Proxy:           nil,
				DialContext: (&net.Dialer{
					Timeout:   10 * time.Second,
					KeepAlive: 1 * time.Second,
				}).DialContext,
				DisableKeepAlives: true,
			}}}

	pf, err := edgectl.CheckedRetryingCommand(p, "traffic-kpf", "kubectl", kpfArgs, tm.check, 15*time.Second)
	if err != nil {
		return nil, err
	}
	tm.crc = pf
	return tm, nil
}

func (tm *trafficManager) check(p *supervisor.Process) error {
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
		resp, err := tm.hClient.Post("http://teleproxy/api/tables/", "application/json", strings.NewReader(body))
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

func (tm *trafficManager) request(method, path string, data []byte) (result string, code int, err error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/%s", tm.apiPort, path)
	req, err := http.NewRequest(method, url, bytes.NewBuffer(data))
	if err != nil {
		return
	}

	req.Header.Set("edgectl-install-id", tm.installID)
	req.Header.Set("edgectl-connect-ci", strconv.FormatBool(tm.connectCI))

	resp, err := tm.hClient.Do(req)
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
func (tm *trafficManager) Name() string {
	return "trafficMgr"
}

// IsOkay implements Resource
func (tm *trafficManager) IsOkay() bool {
	return tm.crc.IsOkay()
}

// Close implements Resource
func (tm *trafficManager) Close() error {
	return tm.crc.Close()
}
