package connector

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"strings"
	"sync/atomic"
	"time"

	"github.com/golang/protobuf/ptypes/empty"

	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"

	"github.com/datawire/telepresence2/pkg/client"
	manager "github.com/datawire/telepresence2/pkg/rpc"
	rpc "github.com/datawire/telepresence2/pkg/rpc/connector"
)

// trafficManager is a handle to access the Traffic Manager in a
// cluster.
type trafficManager struct {
	crc         client.Resource
	apiPort     int32
	sshPort     int32
	userAndHost string
	iiSnapshot  atomic.Value
	aiSnapshot  atomic.Value
	installID   string // telepresence's install ID
	sessionID   string // sessionID returned by the traffic-manager
	tmClient    manager.ManagerClient
	apiErr      error  // holds the latest traffic-manager API error
	previewHost string // hostname to use for preview URLs, if enabled
	connectCI   bool   // whether --ci was passed to connect
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
	ti, err := newTrafficManagerInstaller("", cluster.ctx, cluster.namespace)
	if err != nil {
		return nil, err
	}
	remoteSshPort, remoteApiPort, err := ti.ensure(p, cluster.namespace)
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
		Version:   client.Version(),
	})

	if err != nil {
		conn.Close()
		tm.tmClient = nil
		return err
	}

	tm.sessionID = si.SessionId
	p.Supervisor().Supervise(&supervisor.Worker{
		Name: "watch-agents",
		Work: tm.watchAgents})

	p.Supervisor().Supervise(&supervisor.Worker{
		Name: "watch-intercepts",
		Work: tm.watchIntercepts})
	return err
}

type watchEntry struct {
	data interface{}
	err  error
}

func (tm *trafficManager) watchAgents(p *supervisor.Process) error {
	ac, err := tm.tmClient.WatchAgents(p.Context(), &manager.SessionInfo{SessionId: tm.sessionID})
	if err != nil {
		return err
	}
	p.Ready()
	return watchRecv(p, func() *watchEntry {
		we := new(watchEntry)
		we.data, we.err = ac.Recv()
		return we
	}, &tm.aiSnapshot)
}

func (tm *trafficManager) watchIntercepts(p *supervisor.Process) error {
	ic, err := tm.tmClient.WatchIntercepts(p.Context(), &manager.SessionInfo{SessionId: tm.sessionID})
	if err != nil {
		return err
	}
	p.Ready()
	return watchRecv(p, func() *watchEntry {
		we := new(watchEntry)
		we.data, we.err = ic.Recv()
		return we
	}, &tm.iiSnapshot)
}

func watchRecv(p *supervisor.Process, recv func() *watchEntry, value *atomic.Value) error {
	wc := make(chan *watchEntry)
	closing := false
	go func() {
		// Feed entries into the iic channel
		for {
			we := recv()
			wc <- we
			if we.err != nil || closing {
				break
			}
		}
	}()

	for {
		select {
		case we := <-wc:
			if we.err != nil {
				if we.err == io.EOF {
					return nil
				}
				return we.err
			}
			value.Store(we.data)
		case <-p.Shutdown():
			closing = true
			return nil
		}
	}
}

// addIntercept adds one intercept
func (tm *trafficManager) addIntercept(p *supervisor.Process, ir *manager.CreateInterceptRequest) (*rpc.InterceptResult, error) {
	result := &rpc.InterceptResult{}
	ii, err := tm.tmClient.CreateIntercept(p.Context(), ir)
	if err != nil {
		result.Error = rpc.InterceptError_TRAFFIC_MANAGER_ERROR
		result.ErrorText = err.Error()
		return result, nil
	}
	result.InterceptInfo = ii
	return result, nil
}

// removeIntercept removes one intercept by name
func (tm *trafficManager) removeIntercept(p *supervisor.Process, name string) (*empty.Empty, error) {
	return tm.tmClient.RemoveIntercept(p.Context(), &manager.RemoveInterceptRequest2{
		Session: &manager.SessionInfo{SessionId: tm.sessionID},
		Name:    name,
	})
}

// clearIntercepts removes all intercepts
func (tm *trafficManager) clearIntercepts(p *supervisor.Process) error {
	is := tm.interceptInfoSnapshot()
	if is == nil {
		return nil
	}
	for _, cept := range is.Intercepts {
		_, err := tm.removeIntercept(p, cept.Spec.Name)
		if err != nil {
			return err
		}
	}
	return nil
}

func (tm *trafficManager) agentInfoSnapshot() *manager.AgentInfoSnapshot {
	return tm.aiSnapshot.Load().(*manager.AgentInfoSnapshot)
}

func (tm *trafficManager) interceptInfoSnapshot() *manager.InterceptInfoSnapshot {
	return tm.iiSnapshot.Load().(*manager.InterceptInfoSnapshot)
}

func (tm *trafficManager) check(p *supervisor.Process) error {
	if tm.tmClient == nil {
		// First check. Establish connection
		return tm.initGrpc(p)
	}
	_, err := tm.tmClient.Remain(p.Context(), &manager.SessionInfo{SessionId: tm.sessionID})
	return err
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
