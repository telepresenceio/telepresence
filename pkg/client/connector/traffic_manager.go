package connector

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/user"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/grpc"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/client/auth"
	rpc "github.com/datawire/telepresence2/pkg/rpc/connector"
	"github.com/datawire/telepresence2/pkg/rpc/manager"
)

// intercept represents a live intercept in the form of an ssh port forward.
type intercept struct {
	cancel     context.CancelFunc
	targetHost string
	targetPort int32
}

// trafficManager is a handle to access the Traffic Manager in a
// cluster.
type trafficManager struct {
	*k8sCluster
	aiListener     aiListener
	iiListener     iiListener
	conn           *grpc.ClientConn
	grpc           manager.ManagerClient
	startup        chan bool
	apiPort        int32
	sshPort        int32
	userAndHost    string
	installID      string // telepresence's install ID
	sessionID      string // sessionID returned by the traffic-manager
	apiErr         error  // holds the latest traffic-manager API error
	connectCI      bool   // whether --ci was passed to connect
	installer      *installer
	intercepts     map[string]*intercept
	interceptsLock sync.Mutex
}

// newTrafficManager returns a TrafficManager resource for the given
// cluster if it has a Traffic Manager service.
func newTrafficManager(c context.Context, cluster *k8sCluster, installID string, isCI bool) (*trafficManager, error) {
	userinfo, err := user.Current()
	if err != nil {
		return nil, errors.Wrap(err, "user.Current()")
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, errors.Wrap(err, "os.Hostname()")
	}

	// Ensure that we have a traffic-manager to talk to.
	ti, err := newTrafficManagerInstaller(cluster)
	if err != nil {
		return nil, errors.Wrap(err, "new installer")
	}
	localAPIPort, err := getFreePort()
	if err != nil {
		return nil, errors.Wrap(err, "get free port for API")
	}
	localSSHPort, err := getFreePort()
	if err != nil {
		return nil, errors.Wrap(err, "get free port for ssh")
	}
	tm := &trafficManager{
		k8sCluster:  cluster,
		installer:   ti,
		apiPort:     localAPIPort,
		sshPort:     localSSHPort,
		installID:   installID,
		connectCI:   isCI,
		startup:     make(chan bool),
		userAndHost: fmt.Sprintf("%s@%s", userinfo.Username, host),
		intercepts:  make(map[string]*intercept)}

	dgroup.ParentGroup(c).Go("traffic-manager", tm.start)
	return tm, nil
}

func (tm *trafficManager) waitUntilStarted() error {
	<-tm.startup
	return tm.apiErr
}

func (tm *trafficManager) start(c context.Context) error {
	remoteSSHPort, remoteAPIPort, err := tm.installer.ensureManager(c)
	if err != nil {
		tm.apiErr = err
		close(tm.startup)
		return err
	}
	kpfArgs := []string{
		"port-forward",
		"svc/traffic-manager",
		fmt.Sprintf("%d:%d", tm.sshPort, remoteSSHPort),
		fmt.Sprintf("%d:%d", tm.apiPort, remoteAPIPort)}

	err = client.Retry(c, "svc/traffic-manager port-forward", func(c context.Context) error {
		return tm.installer.portForwardAndThen(c, kpfArgs, "init-grpc", tm.initGrpc)
	}, 2*time.Second, 15*time.Second, time.Minute)
	if err != nil && tm.apiErr == nil {
		tm.apiErr = err
		close(tm.startup)
	}
	return err
}

func (tm *trafficManager) bearerToken() string {
	token, err := auth.LoadTokenFromUserCache()
	if err != nil {
		return ""
	}
	return token.AccessToken
}

func (tm *trafficManager) initGrpc(c context.Context) (err error) {
	defer func() {
		tm.apiErr = err
		close(tm.startup)
	}()

	// First check. Establish connection
	tc, cancel := context.WithTimeout(c, connectTimeout)
	defer cancel()

	var conn *grpc.ClientConn
	conn, err = grpc.DialContext(tc, fmt.Sprintf("127.0.0.1:%d", tm.apiPort),
		grpc.WithInsecure(),
		grpc.WithNoProxy(),
		grpc.WithBlock())
	if err != nil {
		if tc.Err() == context.DeadlineExceeded {
			err = errors.New("timeout when connecting to traffic-manager")
		}
		return err
	}

	mClient := manager.NewManagerClient(conn)
	si, err := mClient.ArriveAsClient(c, &manager.ClientInfo{
		Name:        tm.userAndHost,
		InstallId:   tm.installID,
		Product:     "telepresence",
		Version:     client.Version(),
		BearerToken: tm.bearerToken(),
	})

	if err != nil {
		dlog.Errorf(c, "ArriveAsClient: %v", err)
		conn.Close()
		return err
	}
	tm.conn = conn
	tm.grpc = mClient
	tm.sessionID = si.SessionId

	g := dgroup.ParentGroup(c)
	g.Go("remain", tm.remain)
	g.Go("watch-agents", tm.watchAgents)
	g.Go("watch-intercepts", tm.watchIntercepts)
	return nil
}

func (tm *trafficManager) watchAgents(c context.Context) error {
	ac, err := tm.grpc.WatchAgents(c, tm.session())
	if err != nil {
		return err
	}
	return tm.aiListener.start(c, ac)
}

func (tm *trafficManager) watchIntercepts(c context.Context) error {
	ic, err := tm.grpc.WatchIntercepts(c, tm.session())
	if err != nil {
		return err
	}
	return tm.iiListener.start(c, ic)
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

func (tm *trafficManager) deploymentInfoSnapshot(filter rpc.ListRequest_Filter) *rpc.DeploymentInfoSnapshot {
	var iMap map[string]*manager.InterceptInfo
	if is := tm.interceptInfoSnapshot(); is != nil {
		iMap = make(map[string]*manager.InterceptInfo, len(is.Intercepts))
		for _, i := range is.Intercepts {
			iMap[i.Spec.Agent] = i
		}
	} else {
		iMap = map[string]*manager.InterceptInfo{}
	}
	var aMap map[string]*manager.AgentInfo
	if as := tm.agentInfoSnapshot(); as != nil {
		aMap = make(map[string]*manager.AgentInfo, len(as.Agents))
		for _, a := range as.Agents {
			aMap[a.Name] = a
		}
	} else {
		aMap = map[string]*manager.AgentInfo{}
	}
	depInfos := make([]*rpc.DeploymentInfo, 0)
	for _, depName := range tm.deploymentNames() {
		iCept, ok := iMap[depName]
		if !ok && filter <= rpc.ListRequest_INTERCEPTS {
			continue
		}
		agent, ok := aMap[depName]
		if !ok && filter <= rpc.ListRequest_INSTALLED_AGENTS {
			continue
		}
		reason := ""
		if agent == nil && iCept == nil {
			// Check if interceptable
			dep := tm.findDeployment(depName)
			if dep == nil {
				// Removed from snapshot since the name slice was obtained
				continue
			}
			matchingSvcs := tm.installer.findMatchingServices("", dep)
			if len(matchingSvcs) == 0 {
				if !ok && filter <= rpc.ListRequest_INTERCEPTABLE {
					continue
				}
				reason = "No service with matching selector"
			}
		}

		depInfos = append(depInfos, &rpc.DeploymentInfo{
			Name:                   depName,
			NotInterceptableReason: reason,
			AgentInfo:              aMap[depName],
			InterceptInfo:          iMap[depName],
		})
	}
	return &rpc.DeploymentInfoSnapshot{Deployments: depInfos}
}

func (tm *trafficManager) remain(c context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.Done():
			return nil
		case <-ticker.C:
			_, err := tm.grpc.Remain(c, &manager.RemainRequest{
				Session:     tm.session(),
				BearerToken: tm.bearerToken(),
			})
			if err != nil {
				return err
			}
		}
	}
}

// Close implements io.Closer
func (tm *trafficManager) Close() error {
	if tm.conn != nil {
		_ = tm.conn.Close()
		tm.conn = nil
		tm.grpc = nil
	}
	return nil
}

func (tm *trafficManager) setStatus(r *rpc.ConnectInfo) {
	if tm.grpc == nil {
		r.Intercepts = &manager.InterceptInfoSnapshot{}
		r.Agents = &manager.AgentInfoSnapshot{}
		if err := tm.apiErr; err != nil {
			r.ErrorText = err.Error()
		}
	} else {
		r.Agents = tm.agentInfoSnapshot()
		r.Intercepts = tm.interceptInfoSnapshot()
	}
}

func (tm *trafficManager) uninstall(c context.Context, ur *rpc.UninstallRequest) (*rpc.UninstallResult, error) {
	result := &rpc.UninstallResult{}
	var agents []*manager.AgentInfo
	if ais := tm.agentInfoSnapshot(); ais != nil {
		agents = ais.Agents
	}

	_ = tm.clearIntercepts(c)
	switch ur.UninstallType {
	case rpc.UninstallRequest_UNSPECIFIED:
		return nil, errors.New("invalid uninstall request")
	case rpc.UninstallRequest_NAMED_AGENTS:
		var selectedAgents []*manager.AgentInfo
		for _, di := range ur.Agents {
			found := false
			for _, ai := range agents {
				if di == ai.Name {
					found = true
					selectedAgents = append(selectedAgents, ai)
					break
				}
			}
			if !found {
				result.ErrorText = fmt.Sprintf("unable to find a deployment named %q with an agent installed", di)
			}
		}
		agents = selectedAgents
		fallthrough
	case rpc.UninstallRequest_ALL_AGENTS:
		if len(agents) > 0 {
			if err := tm.installer.removeManagerAndAgents(c, true, agents); err != nil {
				result.ErrorText = err.Error()
			}
		}
	default:
		// Cancel all communication with the manager
		_ = tm.Close()
		if err := tm.installer.removeManagerAndAgents(c, false, agents); err != nil {
			result.ErrorText = err.Error()
		}
	}
	return result, nil
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
func (r *watcher) watch(c context.Context) error {
	dataChan := make(chan interface{}, 1000)
	defer close(dataChan)

	done := int32(0)
	go func() {
		for {
			select {
			case <-c.Done():
				// ensure no more writes and drain channel to unblock writer
				atomic.StoreInt32(&done, 1)
				for range dataChan {
				}
				return
			case data := <-dataChan:
				if data == nil {
					return
				}

				r.listenersLock.RLock()
				lc := make([]listener, len(r.listeners))
				copy(lc, r.listeners)
				r.listenersLock.RUnlock()

				for _, l := range lc {
					l.onData(data)
				}
			}
		}
	}()

	var err error
	for {
		data := r.entryMaker()
		if err = r.stream.RecvMsg(data); err != nil {
			if err == io.EOF || strings.HasSuffix(err.Error(), " is closing") {
				err = nil
			}
			break
		}
		if atomic.LoadInt32(&done) != 0 {
			break
		}
		dataChan <- data
		if atomic.LoadInt32(&done) != 0 {
			break
		}
	}
	return err
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

func (al *aiListener) start(c context.Context, stream grpc.ClientStream) error {
	al.stream = stream
	al.listeners = []listener{al}
	al.entryMaker = func() interface{} { return new(manager.AgentInfoSnapshot) }
	return al.watch(c)
}

func (il *iiListener) onData(d interface{}) {
	il.data.Store(d)
}

func (il *iiListener) start(c context.Context, stream grpc.ClientStream) error {
	il.stream = stream
	il.listeners = []listener{il}
	il.entryMaker = func() interface{} { return new(manager.InterceptInfoSnapshot) }
	return il.watch(c)
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
				done := ia.done
				ia.done = nil
				if done != nil {
					done <- ii
					close(done)
				}
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
				done := ap.done
				ap.done = nil
				if done != nil {
					done <- ai
					close(done)
				}
				break
			}
		}
	}
}
