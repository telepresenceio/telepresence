package connector

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/user"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/grpc"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/datawire/telepresence2/rpc/v2/connector"
	"github.com/datawire/telepresence2/rpc/v2/manager"
	"github.com/datawire/telepresence2/v2/pkg/client"
	"github.com/datawire/telepresence2/v2/pkg/client/actions"
	"github.com/datawire/telepresence2/v2/pkg/client/cache"
)

// trafficManager is a handle to access the Traffic Manager in a
// cluster.
type trafficManager struct {
	// local information
	env         client.Env
	installID   string // telepresence's install ID
	userAndHost string // "laptop-username@laptop-hostname"

	// k8s client
	k8sClient *k8sCluster

	// manager client
	managerClient manager.ManagerClient
	managerErr    error     // if managerClient is nil, why it's nil
	startup       chan bool // gets closed when managerClient is fully initialized (or managerErr is set)

	sessionInfo *manager.SessionInfo // sessionInfo returned by the traffic-manager

	// sshPort is a local TCP port number that the userd uses internally that gets forwarded to
	// the SSH port on the manager Pod.
	//
	// FIXME(lukeshu): sshPort is exposed to the rest of the machine because we use separate
	// `kubectl port-forward` and `ssh -D` processes; it should go away by way of moving the
	// port-forwarding to happen in the userd process.
	sshPort int32

	installer *installer

	// Map of desired mount points for intercepts
	mountPoints sync.Map
}

// newTrafficManager returns a TrafficManager resource for the given
// cluster if it has a Traffic Manager service.
func newTrafficManager(c context.Context, env client.Env, cluster *k8sCluster, installID string) (*trafficManager, error) {
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
	tm := &trafficManager{
		env:         env,
		k8sClient:   cluster,
		installer:   ti,
		installID:   installID,
		startup:     make(chan bool),
		userAndHost: fmt.Sprintf("%s@%s", userinfo.Username, host),
	}

	dgroup.ParentGroup(c).Go("traffic-manager", tm.run)
	return tm, nil
}

func (tm *trafficManager) waitUntilStarted() error {
	<-tm.startup
	return tm.managerErr
}

func (tm *trafficManager) run(c context.Context) error {
	err := tm.installer.ensureManager(c, tm.env)
	if err != nil {
		tm.managerErr = err
		close(tm.startup)
		return err
	}

	// Ensure that startup is made within a minute. Context must not be cancelled when the startup
	// succeeds.
	c, cancel := context.WithCancel(c)
	defer cancel()
	go func() {
		select {
		case <-tm.startup:
			return
		case <-time.After(time.Minute):
			cancel()
		}
	}()

	kpfArgs := []string{
		"svc/traffic-manager",
		fmt.Sprintf(":%d", ManagerPortSSH),
		fmt.Sprintf(":%d", ManagerPortHTTP)}

	// Scan port-forward output and grab the dynamically allocated ports
	rxPortForward := regexp.MustCompile(`\AForwarding from \d+\.\d+\.\d+\.\d+:(\d+) -> (\d+)`)
	outputScanner := func(sc *bufio.Scanner) interface{} {
		var sshPort, apiPort string
		for sc.Scan() {
			if rxr := rxPortForward.FindStringSubmatch(sc.Text()); rxr != nil {
				toPort, _ := strconv.Atoi(rxr[2])
				if toPort == ManagerPortSSH {
					sshPort = rxr[1]
					dlog.Debugf(c, "traffic-manager ssh-port %s", sshPort)
				} else if toPort == ManagerPortHTTP {
					apiPort = rxr[1]
					dlog.Debugf(c, "traffic-manager api-port %s", apiPort)
				}
				if sshPort != "" && apiPort != "" {
					return []string{sshPort, apiPort}
				}
			}
		}
		return nil
	}

	err = client.Retry(c, "svc/traffic-manager port-forward", func(c context.Context) error {
		return tm.installer.portForwardAndThen(c, kpfArgs, outputScanner, "init-grpc", tm.initGrpc)
	}, 2*time.Second, 15*time.Second)

	if err != nil && tm.managerErr == nil {
		tm.managerErr = err
		close(tm.startup)
	}
	return err
}

func (tm *trafficManager) bearerToken() string {
	token, err := cache.LoadTokenFromUserCache()
	if err != nil {
		return ""
	}
	return token.AccessToken
}

func (tm *trafficManager) initGrpc(c context.Context, portsIf interface{}) (err error) {
	defer func() {
		tm.managerErr = err
		close(tm.startup)
	}()

	ports := portsIf.([]string)
	sshPort, _ := strconv.Atoi(ports[0])
	tm.sshPort = int32(sshPort)

	// First check. Establish connection
	tc, cancel := context.WithTimeout(c, connectTimeout)
	defer cancel()

	var conn *grpc.ClientConn
	conn, err = grpc.DialContext(tc, "127.0.0.1:"+ports[1],
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
	tm.managerClient = mClient
	tm.sessionInfo = si

	g := dgroup.ParentGroup(c)
	g.Go("remain", tm.remain)
	g.Go("intercept-port-forward", tm.workerPortForwardIntercepts)
	return nil
}

func (tm *trafficManager) session() *manager.SessionInfo {
	return tm.sessionInfo
}

func (tm *trafficManager) deploymentInfoSnapshot(ctx context.Context, filter rpc.ListRequest_Filter) *rpc.DeploymentInfoSnapshot {
	var iMap map[string]*manager.InterceptInfo
	if is, _ := actions.ListMyIntercepts(ctx, tm.managerClient, tm.session().SessionId); is != nil {
		iMap = make(map[string]*manager.InterceptInfo, len(is))
		for _, i := range is {
			iMap[i.Spec.Agent] = i
		}
	} else {
		iMap = map[string]*manager.InterceptInfo{}
	}
	var aMap map[string]*manager.AgentInfo
	if as, _ := actions.ListAllAgents(ctx, tm.managerClient, tm.session().SessionId); as != nil {
		aMap = make(map[string]*manager.AgentInfo, len(as))
		for _, a := range as {
			aMap[a.Name] = a
		}
	} else {
		aMap = map[string]*manager.AgentInfo{}
	}
	depInfos := make([]*rpc.DeploymentInfo, 0)
	for _, depName := range tm.k8sClient.deploymentNames() {
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
			dep := tm.k8sClient.findDeployment(depName)
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
			_ = tm.clearIntercepts(context.Background())
			_, _ = tm.managerClient.Depart(context.Background(), tm.session())
			return nil
		case <-ticker.C:
			_, err := tm.managerClient.Remain(c, &manager.RemainRequest{
				Session:     tm.session(),
				BearerToken: tm.bearerToken(),
			})
			if err != nil {
				if c.Err() != nil {
					err = nil
				}
				return err
			}
		}
	}
}

func (tm *trafficManager) setStatus(ctx context.Context, r *rpc.ConnectInfo) {
	if tm.managerClient == nil {
		r.Intercepts = &manager.InterceptInfoSnapshot{}
		r.Agents = &manager.AgentInfoSnapshot{}
		if err := tm.managerErr; err != nil {
			r.ErrorText = err.Error()
		}
	} else {
		agents, _ := actions.ListAllAgents(ctx, tm.managerClient, tm.session().SessionId)
		intercepts, _ := actions.ListMyIntercepts(ctx, tm.managerClient, tm.session().SessionId)
		r.Agents = &manager.AgentInfoSnapshot{Agents: agents}
		r.Intercepts = &manager.InterceptInfoSnapshot{Intercepts: intercepts}
		r.SessionInfo = tm.session()
	}
}

func (tm *trafficManager) uninstall(c context.Context, ur *rpc.UninstallRequest) (*rpc.UninstallResult, error) {
	result := &rpc.UninstallResult{}
	agents, _ := actions.ListAllAgents(c, tm.managerClient, tm.session().SessionId)

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
		if err := tm.installer.removeManagerAndAgents(c, false, agents); err != nil {
			result.ErrorText = err.Error()
		}
	}
	return result, nil
}
