package connector

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"strings"
	"sync"
	"sync/atomic"
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
	crc         client.Resource
	aiListener  aiListener
	iiListener  iiListener
	grpc        manager.ManagerClient
	apiPort     int32
	sshPort     int32
	userAndHost string
	installID   string // telepresence's install ID
	sessionID   string // sessionID returned by the traffic-manager
	apiErr      error  // holds the latest traffic-manager API error
	previewHost string // hostname to use for preview URLs, if enabled
	connectCI   bool   // whether --ci was passed to connect
	installer   *installer
	cept        *intercept
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
	remoteSshPort, remoteApiPort, err := ti.ensureManager(p)
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
		installer:   ti,
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

	tm.grpc = manager.NewManagerClient(conn)
	si, err := tm.grpc.ArriveAsClient(p.Context(), &manager.ClientInfo{
		Name:      tm.userAndHost,
		InstallId: tm.installID,
		Product:   "telepresence",
		Version:   client.Version(),
	})

	if err != nil {
		conn.Close()
		tm.grpc = nil
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

func (tm *trafficManager) watchAgents(p *supervisor.Process) error {
	ac, err := tm.grpc.WatchAgents(p.Context(), tm.session())
	if err != nil {
		return err
	}

	p.Ready()
	return tm.aiListener.start(ac)
}

func (tm *trafficManager) watchIntercepts(p *supervisor.Process) error {
	ic, err := tm.grpc.WatchIntercepts(p.Context(), tm.session())
	if err != nil {
		return err
	}

	p.Ready()
	return tm.iiListener.start(ic)
}

func (tm *trafficManager) session() *manager.SessionInfo {
	return &manager.SessionInfo{SessionId: tm.sessionID}
}

func (tm *trafficManager) agentInfoSnapshot() *manager.AgentInfoSnapshot {
	return tm.aiListener.getData()
}

func (tm *trafficManager) interceptInfoSnapshot() *manager.InterceptInfoSnapshot {
	return tm.iiListener.getData()
}

func (tm *trafficManager) check(p *supervisor.Process) error {
	if tm.grpc == nil {
		// First check. Establish connection
		return tm.initGrpc(p)
	}
	_, err := tm.grpc.Remain(p.Context(), tm.session())
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

// A watcher listens on a grpc.ClientStream and notifies listeners when
// something arrives.
type watcher struct {
	entryMaker    func() interface{} // returns an instance of the type produced by the stream
	listeners     []listener
	listenersLock sync.RWMutex
	stream        grpc.ClientStream
}

// watch reads messages from the stream and passes them onto registered listeners. The
// function terminates when the context used when the stream was acquired is cancelled,
// when io.EOF is encountered, or an error occurs during read.
func (r *watcher) watch() error {
	for {
		data := r.entryMaker()
		if err := r.stream.RecvMsg(data); err != nil {
			if err == io.EOF {
				err = nil
			}
			return err
		}

		r.listenersLock.RLock()
		for _, l := range r.listeners {
			go l.onData(data)
		}
		r.listenersLock.RUnlock()
	}
}

func (r *watcher) addListener(l listener) {
	r.listenersLock.Lock()
	r.listeners = append(r.listeners, l)
	r.listenersLock.Unlock()
}

func (r *watcher) removeListener(l listener) {
	r.listenersLock.Lock()
	ls := r.listeners
	for i, x := range ls {
		if l == x {
			last := len(ls) - 1
			ls[i] = ls[last]
			ls[last] = nil
			r.listeners = ls[:last]
			break
		}
	}
	r.listenersLock.Unlock()
}

// A listener gets notified by a watcher when something arrives on the stream
type listener interface {
	onData(data interface{})
}

// An aiListener keeps track of the latest received AgentInfoSnapshot and provides the
// watcher needed to register other listeners.
type aiListener struct {
	watcher
	data atomic.Value
}

func (al *aiListener) getData() *manager.AgentInfoSnapshot {
	v := al.data.Load()
	if v == nil {
		return nil
	}
	return v.(*manager.AgentInfoSnapshot)
}

func (al *aiListener) onData(d interface{}) {
	al.data.Store(d)
}

func (al *aiListener) start(stream manager.Manager_WatchAgentsClient) error {
	al.stream = stream
	al.listeners = []listener{al}
	al.entryMaker = func() interface{} { return new(manager.AgentInfoSnapshot) }
	return al.watch()
}

func (il *iiListener) onData(d interface{}) {
	il.data.Store(d)
}

func (il *iiListener) start(stream manager.Manager_WatchInterceptsClient) error {
	il.stream = stream
	il.listeners = []listener{il}
	il.entryMaker = func() interface{} { return new(manager.InterceptInfoSnapshot) }
	return il.watch()
}

// iiActive is a listener that waits for an intercept with a given id to become active
type iiActive struct {
	id   string
	done chan *manager.InterceptInfo
}

func (ia *iiActive) onData(d interface{}) {
	if iis, ok := d.(*manager.InterceptInfoSnapshot); ok {
		for _, ii := range iis.Intercepts {
			if ii.Id == ia.id && ii.Disposition != manager.InterceptDispositionType_WAITING {
				ia.done <- ii
				break
			}
		}
	}
}

// aiPresent is a listener that waits for an agent with a given name to be present
type aiPresent struct {
	name string
	done chan *manager.AgentInfo
}

func (ap *aiPresent) onData(d interface{}) {
	if ais, ok := d.(*manager.AgentInfoSnapshot); ok {
		for _, ai := range ais.Agents {
			if ai.Name == ap.name {
				ap.done <- ai
				break
			}
		}
	}
}
