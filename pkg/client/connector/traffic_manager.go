package connector

import (
	"fmt"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"

	"github.com/datawire/telepresence2/pkg/client"
	manager "github.com/datawire/telepresence2/pkg/rpc"
)

// trafficManager is a handle to access the Traffic Manager in a
// cluster.
type trafficManager struct {
	crc            client.Resource
	apiPort        int32
	sshPort        int32
	userAndHost    string
	interceptables []string
	totalClusCepts int
	installID      string // telepresence's install ID
	sessionID      string // sessionID returned by the traffic-manager
	tmClient       manager.ManagerClient
	apiErr         error  // holds the latest traffic-manager API error
	licenseInfo    string // license information from traffic-manager
	previewHost    string // hostname to use for preview URLs, if enabled
	connectCI      bool   // whether --ci was passed to connect
}

// newTrafficManager returns a TrafficManager resource for the given
// cluster if it has a Traffic Manager service.
func newTrafficManager(p *supervisor.Process, cluster *k8sCluster, installID string, isCI bool) (*trafficManager, error) {
	name, err := user.Current()
	if err != nil {
		return nil, errors.Wrap(err, "user.Current()")
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, errors.Wrap(err, "os.Hostname()")
	}

	// Ensure that we have a traffic-manager to talk to.
	remoteSshPort, remoteApiPort, err := ensureTrafficManager(p)
	if err != nil {
		return nil, err
	}

	localApiPort, err := getFreePort()
	if err != nil {
		return nil, errors.Wrap(err, "get free port for API")
	}
	localSshPort, err := getFreePort()
	if err != nil {
		return nil, errors.Wrap(err, "get free port for ssh")
	}

	kpfArgStr := fmt.Sprintf("port-forward svc/traffic-manager %d:%d %d:%d", localSshPort, remoteSshPort, localApiPort, remoteApiPort)
	kpfArgs := cluster.getKubectlArgs(strings.Fields(kpfArgStr)...)
	tm := &trafficManager{
		apiPort:     localApiPort,
		sshPort:     localSshPort,
		installID:   installID,
		connectCI:   isCI,
		userAndHost: fmt.Sprintf("%s@%s", name, host)}

	pf, err := client.CheckedRetryingCommand(p, "traffic-kpf", "kubectl", kpfArgs, tm.check, 15*time.Second)
	if err != nil {
		return nil, err
	}
	tm.crc = pf
	return tm, nil
}

func (tm *trafficManager) initGrpc(p *supervisor.Process) error {
	// First check. Establish connection
	conn, err := grpc.Dial(fmt.Sprintf("127.0.0.1:%d", tm.apiPort), grpc.WithInsecure())
	if err != nil {
		return err
	}

	// Wait until connection is ready
	for {
		state := conn.GetState()
		switch state {
		case connectivity.Idle, connectivity.Ready:
			// Do nothing. We'll break out of the loop after the switch.
		case connectivity.Connecting:
			time.Sleep(10 * time.Millisecond)
			continue
		default:
			return fmt.Errorf("connection state: %s", state.String())
		}
		break
	}

	tm.tmClient = manager.NewManagerClient(conn)
	si, err := tm.tmClient.ArriveAsClient(p.Context(), &manager.ClientInfo{
		Name:      tm.userAndHost,
		InstallId: tm.installID,
		Product:   "telepresence",
		Version:   client.Version,
	})

	if err == nil {
		tm.sessionID = si.SessionId
	} else {
		conn.Close()
		tm.tmClient = nil
	}
	return err
}

func (tm *trafficManager) check(p *supervisor.Process) error {
	if tm.tmClient == nil {
		// First check. Establish connection
		return tm.initGrpc(p)
	}
	_, err := tm.tmClient.Remain(p.Context(), &manager.SessionInfo{SessionId: tm.sessionID})
	return err
	/*
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
		return nil
	*/
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

// request part of old REST API, about to be removed
func (_ *trafficManager) request(_ string, _ string, _ []byte) (string, int, error) {
	return "", 404, nil
}
