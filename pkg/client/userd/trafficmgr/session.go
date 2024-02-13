package trafficmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	empty "google.golang.org/protobuf/types/known/emptypb"
	"gopkg.in/yaml.v3"
	core "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/homedir"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/rpc/v2/authenticator"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	rootdRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	authGrpc "github.com/telepresenceio/telepresence/v2/pkg/authenticator/grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator/patcher"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8sclient"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/matcher"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
)

type apiServer struct {
	restapi.Server
	cancel context.CancelFunc
}

type apiMatcher struct {
	requestMatcher matcher.Request
	metadata       map[string]string
}

type session struct {
	*k8s.Cluster
	rootDaemon         rootdRpc.DaemonClient
	subnetViaWorkloads []*rootdRpc.SubnetViaWorkload

	// local information
	installID   string // telepresence's install ID
	userAndHost string // "laptop-username@laptop-hostname"

	// Kubernetes Port Forward Dialer
	pfDialer dnet.PortForwardDialer

	// manager client
	managerClient manager.ManagerClient

	// manager client connection
	managerConn *grpc.ClientConn

	// name reported by the manager
	managerName string

	// version reported by the manager
	managerVersion semver.Version

	// The identifier for this daemon
	daemonID *daemon.Identifier

	sessionInfo *manager.SessionInfo // sessionInfo returned by the traffic-manager

	wlWatcher *workloadsAndServicesWatcher

	// currentInterceptsLock ensures that all accesses to currentIntercepts, currentMatchers,
	// currentAPIServers, interceptWaiters, and ingressInfo are synchronized
	//
	currentInterceptsLock sync.Mutex

	// currentIntercepts is the latest snapshot returned by the intercept watcher. It
	// is keyeed by the intercept ID
	currentIntercepts map[string]*intercept

	// currentMatches hold the matchers used when using the APIServer.
	currentMatchers map[string]*apiMatcher

	// currentAPIServers contains the APIServer in use. Typically zero or only one, but since the
	// port is determined by the intercept, there might theoretically be serveral.
	currentAPIServers map[int]*apiServer

	// Map of desired awaited intercepts. Keyed by intercept name, because it
	// is filled in prior to the intercept being created. Entries are short lived. They
	// are deleted as soon as the intercept arrives and gets stored in currentIntercepts
	interceptWaiters map[string]*awaitIntercept

	ingressInfo []*manager.IngressInfo

	isPodDaemon bool

	sessionConfig client.Config

	// done is closed when the session ends
	done chan struct{}

	// Possibly extended version of the session. Use when calling interface methods.
	self userd.Session
}

func NewSession(
	ctx context.Context,
	cr *rpc.ConnectRequest,
	config *client.Kubeconfig,
) (_ context.Context, _ userd.Session, info *connector.ConnectInfo) {
	dlog.Info(ctx, "-- Starting new session")

	connectStart := time.Now()
	defer func() {
		if info.Error == connector.ConnectInfo_UNSPECIFIED {
			scout.Report(ctx, "connect",
				scout.Entry{
					Key:   "time_to_connect",
					Value: time.Since(connectStart).Seconds(),
				}, scout.Entry{
					Key:   "mapped_namespaces",
					Value: len(cr.MappedNamespaces),
				})
		} else {
			scout.Report(ctx, "connect_error",
				scout.Entry{
					Key:   "error",
					Value: info.ErrorText,
				}, scout.Entry{
					Key:   "error_type",
					Value: info.Error.String(),
				}, scout.Entry{
					Key:   "error_category",
					Value: info.ErrorCategory,
				}, scout.Entry{
					Key:   "time_to_fail",
					Value: time.Since(connectStart).Seconds(),
				}, scout.Entry{
					Key:   "mapped_namespaces",
					Value: len(cr.MappedNamespaces),
				})
		}
	}()

	dlog.Info(ctx, "Connecting to k8s cluster...")
	cluster, err := k8s.ConnectCluster(ctx, cr, config)
	if err != nil {
		dlog.Errorf(ctx, "unable to track k8s cluster: %+v", err)
		return ctx, nil, connectError(rpc.ConnectInfo_CLUSTER_FAILED, err)
	}
	dlog.Infof(ctx, "Connected to context %s, namespace %s (%s)", cluster.Context, cluster.Namespace, cluster.Server)

	ctx = cluster.WithK8sInterface(ctx)
	scout.SetMetadatum(ctx, "cluster_id", cluster.GetClusterId(ctx))

	dlog.Info(ctx, "Connecting to traffic manager...")
	tmgr, err := connectMgr(ctx, cluster, scout.InstallID(ctx), cr)
	if err != nil {
		dlog.Errorf(ctx, "Unable to connect to session: %s", err)
		return ctx, nil, connectError(rpc.ConnectInfo_TRAFFIC_MANAGER_FAILED, err)
	}

	// store session in ctx for reporting
	ctx = scout.WithSession(ctx, tmgr)

	tmgr.sessionConfig = client.GetDefaultConfig()
	cliCfg, err := tmgr.managerClient.GetClientConfig(ctx, &empty.Empty{})
	if err != nil {
		if status.Code(err) != codes.Unimplemented {
			dlog.Warnf(ctx, "Failed to get remote config from traffic manager: %v", err)
		}
	} else {
		if err := yaml.Unmarshal(cliCfg.ConfigYaml, tmgr.sessionConfig); err != nil {
			dlog.Warnf(ctx, "Failed to deserialize remote config: %v", err)
		}
		if err := tmgr.ApplyConfig(ctx); err != nil {
			dlog.Warnf(ctx, "failed to apply config from traffic-manager: %v", err)
		}
		if err := cluster.AddRemoteKubeConfigExtension(ctx, cliCfg.ConfigYaml); err != nil {
			dlog.Warnf(ctx, "Failed to set remote kubeconfig values: %v", err)
		}
	}
	ctx = dnet.WithPortForwardDialer(ctx, tmgr.pfDialer)

	oi := tmgr.getOutboundInfo(ctx, cr)
	rootRunning := userd.GetService(ctx).RootSessionInProcess()
	if !rootRunning {
		// Connect to the root daemon if it is running. It's the CLI that starts it initially
		rootRunning, err = socket.IsRunning(ctx, socket.RootDaemonPath(ctx))
		if err != nil {
			return ctx, nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
		}

		if rootRunning && client.GetConfig(ctx).Cluster().ConnectFromRootDaemon {
			// Root daemon needs this to authenticate with the cluster. Potential exec configurations in the kubeconfig
			// must be executed by the user, not by root.
			konfig, err := patcher.CreateExternalKubeConfig(ctx, cluster.EffectiveFlagMap, func([]string) (string, string, error) {
				s := userd.GetService(ctx)
				if _, ok := s.Server().GetServiceInfo()[authenticator.Authenticator_ServiceDesc.ServiceName]; !ok {
					authGrpc.RegisterAuthenticatorServer(s.Server(), config.ClientConfig)
				}
				return client.GetExe(ctx), s.ListenerAddress(ctx), nil
			}, nil)
			if err != nil {
				return ctx, nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
			}
			patcher.AnnotateOutboundInfo(ctx, oi, konfig.CurrentContext)
		}
	}

	var daemonStatus *rootdRpc.DaemonStatus
	if rootRunning {
		tmgr.rootDaemon, err = tmgr.connectRootDaemon(ctx, oi)
		if err != nil {
			tmgr.managerConn.Close()
			return ctx, nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
		}
		daemonStatus, err = tmgr.rootDaemon.Status(ctx, &empty.Empty{})
		if err != nil {
			return ctx, nil, connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
		}
	} else {
		dlog.Info(ctx, "Root daemon is not running")
	}

	// Collect data on how long connection time took
	dlog.Debug(ctx, "Finished connecting to traffic manager")

	tmgr.AddNamespaceListener(ctx, tmgr.updateDaemonNamespaces)
	info = &rpc.ConnectInfo{
		Error:            rpc.ConnectInfo_UNSPECIFIED,
		ClusterContext:   cluster.Kubeconfig.Context,
		ClusterServer:    cluster.Kubeconfig.Server,
		ClusterId:        cluster.GetClusterId(ctx),
		ManagerInstallId: cluster.GetManagerInstallId(ctx),
		SessionInfo:      tmgr.SessionInfo(),
		ConnectionName:   tmgr.daemonID.Name,
		KubeFlags:        tmgr.OriginalFlagMap,
		Namespace:        cluster.Namespace,
		Intercepts:       &manager.InterceptInfoSnapshot{Intercepts: tmgr.getCurrentInterceptInfos()},
		ManagerNamespace: cluster.Kubeconfig.GetManagerNamespace(),
		DaemonStatus:     daemonStatus,
	}
	return ctx, tmgr, info
}

// SetSelf is for internal use by extensions.
func (s *session) SetSelf(self userd.Session) {
	s.self = self
}

// RunSession (1) starts up with ensuring that the manager is installed and running,
// but then for most of its life
//   - (2) calls manager.ArriveAsClient and then periodically calls manager.Remain
//   - run the intercepts (manager.WatchIntercepts) and then
//   - (3) listen on the appropriate local ports and forward them to the intercepted
//     Services, and
//   - (4) mount the appropriate remote volumes.
func (s *session) RunSession(c context.Context) error {
	self := s.self
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	defer func() {
		self.Epilog(c)
	}()
	self.StartServices(g)
	return g.Wait()
}

func (s *session) RootDaemon() rootdRpc.DaemonClient {
	return s.rootDaemon
}

func (s *session) ManagerClient() manager.ManagerClient {
	return s.managerClient
}

func (s *session) ManagerConn() *grpc.ClientConn {
	return s.managerConn
}

func (s *session) ManagerName() string {
	return s.managerName
}

func (s *session) ManagerVersion() semver.Version {
	return s.managerVersion
}

func (s *session) getSessionConfig() client.Config {
	return s.sessionConfig
}

// connectMgr returns a session for the given cluster that is connected to the traffic-manager.
func connectMgr(
	ctx context.Context,
	cluster *k8s.Cluster,
	installID string,
	cr *rpc.ConnectRequest,
) (*session, error) {
	tos := client.GetConfig(ctx).Timeouts()

	ctx, cancel := tos.TimeoutContext(ctx, client.TimeoutTrafficManagerConnect)
	defer cancel()

	userinfo, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("unable to obtain current user: %w", err)
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("unable to obtain hostname: %w", err)
	}

	err = CheckTrafficManagerService(ctx, cluster.GetManagerNamespace())
	if err != nil {
		return nil, err
	}

	dlog.Debug(ctx, "creating port-forward")
	pfDialer, err := dnet.NewK8sPortForwardDialer(ctx, cluster.Kubeconfig.RestConfig, k8sapi.GetK8sInterface(ctx))
	if err != nil {
		return nil, err
	}
	conn, mClient, vi, err := k8sclient.ConnectToManager(ctx, cluster.GetManagerNamespace(), pfDialer.Dial)
	if err != nil {
		return nil, err
	}
	managerVersion, err := semver.Parse(strings.TrimPrefix(vi.Version, "v"))
	if err != nil {
		return nil, fmt.Errorf("unable to parse manager.Version: %w", err)
	}

	userAndHost := fmt.Sprintf("%s@%s", userinfo.Username, host)

	daemonID, err := daemon.NewIdentifier(cr.Name, cluster.Context, cluster.Namespace, proc.RunningInContainer())
	if err != nil {
		return nil, err
	}
	si, err := LoadSessionInfoFromUserCache(ctx, daemonID)
	if err != nil {
		return nil, err
	}

	svc := userd.GetService(ctx)
	if si != nil {
		// Check if the session is still valid in the traffic-manager by calling Remain
		_, err = mClient.Remain(ctx, &manager.RemainRequest{Session: si})
		if err == nil {
			if ctx.Err() != nil {
				// Call timed out, so the traffic-manager isn't responding at all
				return nil, ctx.Err()
			}
			dlog.Debugf(ctx, "traffic-manager port-forward established, client was already known to the traffic-manager as %q", userAndHost)
		} else {
			si = nil
		}
	}

	if si == nil {
		dlog.Debugf(ctx, "traffic-manager port-forward established, making client known to the traffic-manager as %q", userAndHost)
		si, err = mClient.ArriveAsClient(ctx, &manager.ClientInfo{
			Name:      userAndHost,
			Namespace: cluster.Namespace,
			InstallId: installID,
			Product:   "telepresence",
			Version:   client.Version(),
		})
		if err != nil {
			return nil, client.CheckTimeout(ctx, fmt.Errorf("manager.ArriveAsClient: %w", err))
		}
		if err = SaveSessionInfoToUserCache(ctx, daemonID, si); err != nil {
			return nil, err
		}
	}

	var opts []grpc.CallOption
	cfg := client.GetConfig(ctx)
	if mz := cfg.Grpc().MaxReceiveSize(); mz > 0 {
		opts = append(opts, grpc.MaxCallRecvMsgSize(int(mz)))
	}
	svc.SetManagerClient(mClient, opts...)

	managerName := vi.Name
	if managerName == "" {
		// Older traffic-managers doesn't distinguish between OSS and pro versions
		managerName = "Traffic Manager"
	}

	extraAlsoProxy, err := parseCIDR(cr.GetAlsoProxy())
	if err != nil {
		return nil, fmt.Errorf("failed to parse extra also proxy: %w", err)
	}

	extraNeverProxy, err := parseCIDR(cr.GetNeverProxy())
	if err != nil {
		return nil, fmt.Errorf("failed to parse extra never proxy: %w", err)
	}

	extraAllow, err := parseCIDR(cr.GetAllowConflictingSubnets())
	if err != nil {
		return nil, fmt.Errorf("failed to parse extra allow conflicting subnets: %w", err)
	}

	cluster.AlsoProxy = append(cluster.AlsoProxy, extraAlsoProxy...)
	cluster.NeverProxy = append(cluster.NeverProxy, extraNeverProxy...)
	cluster.AllowConflictingSubnets = append(cluster.AllowConflictingSubnets, extraAllow...)

	sess := &session{
		Cluster:            cluster,
		installID:          installID,
		daemonID:           daemonID,
		userAndHost:        userAndHost,
		managerClient:      mClient,
		managerConn:        conn,
		pfDialer:           pfDialer,
		managerName:        managerName,
		managerVersion:     managerVersion,
		sessionInfo:        si,
		interceptWaiters:   make(map[string]*awaitIntercept),
		wlWatcher:          newWASWatcher(),
		isPodDaemon:        cr.IsPodDaemon,
		done:               make(chan struct{}),
		subnetViaWorkloads: cr.SubnetViaWorkloads,
	}
	sess.self = sess
	return sess, nil
}

func (s *session) NewRemainRequest() *manager.RemainRequest {
	return &manager.RemainRequest{Session: s.SessionInfo()}
}

func (s *session) Remain(ctx context.Context) error {
	self := s.self
	ctx, cancel := client.GetConfig(ctx).Timeouts().TimeoutContext(ctx, client.TimeoutTrafficManagerAPI)
	defer cancel()
	_, err := self.ManagerClient().Remain(ctx, self.NewRemainRequest())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			// Session has expired. We need to cancel the owner session and reconnect
			return ErrSessionExpired
		}
		dlog.Errorf(ctx, "error calling Remain: %v", client.CheckTimeout(ctx, err))
	}
	return nil
}

func parseCIDR(cidr []string) ([]*iputil.Subnet, error) {
	result := make([]*iputil.Subnet, 0)

	if cidr == nil {
		return result, nil
	}

	for i := range cidr {
		_, ipNet, err := net.ParseCIDR(cidr[i])
		if err != nil {
			return nil, fmt.Errorf("failed to parse CIDR %s: %w", cidr[i], err)
		}
		result = append(result, (*iputil.Subnet)(ipNet))
	}

	return result, nil
}

func CheckTrafficManagerService(ctx context.Context, namespace string) error {
	dlog.Debug(ctx, "checking that traffic-manager exists")
	coreV1 := k8sapi.GetK8sInterface(ctx).CoreV1()
	if _, err := coreV1.Services(namespace).Get(ctx, "traffic-manager", meta.GetOptions{}); err != nil {
		msg := fmt.Sprintf("unable to get service traffic-manager in %s: %v", namespace, err)
		se := &k8serrors.StatusError{}
		if errors.As(err, &se) {
			if se.Status().Code == http.StatusNotFound {
				msg = "traffic manager not found, if it is not installed, please run 'telepresence helm install'. " +
					"If it is installed, try connecting with a --manager-namespace to point telepresence to the namespace it's installed in."
			}
		}
		return errcat.User.New(msg)
	}
	return nil
}

func connectError(t rpc.ConnectInfo_ErrType, err error) *rpc.ConnectInfo {
	st := status.Convert(err)
	for _, detail := range st.Details() {
		if detail, ok := detail.(*common.Result); ok {
			return &rpc.ConnectInfo{
				Error:         t,
				ErrorText:     string(detail.Data),
				ErrorCategory: int32(detail.ErrorCategory),
			}
		}
	}
	return &rpc.ConnectInfo{
		Error:         t,
		ErrorText:     err.Error(),
		ErrorCategory: int32(errcat.GetCategory(err)),
	}
}

// updateDaemonNamespacesLocked will create a new DNS search path from the given namespaces and
// send it to the DNS-resolver in the daemon.
func (s *session) updateDaemonNamespaces(c context.Context) {
	s.wlWatcher.setNamespacesToWatch(c, s.GetCurrentNamespaces(true))
	if s.rootDaemon == nil {
		return
	}
	var namespaces []string
	if s.Namespace != "" {
		namespaces = []string{s.Namespace}
	}
	// Avoid being locked for the remainder of this function.

	// Pass current mapped namespaces as plain names (no ending dot). The DNS-resolver will
	// create special mapping for those, allowing names like myservice.mynamespace to be resolved
	paths := s.GetCurrentNamespaces(false)
	dlog.Debugf(c, "posting search paths %v and namespaces %v", paths, namespaces)

	if _, err := s.rootDaemon.SetDnsSearchPath(c, &rootdRpc.Paths{Paths: paths, Namespaces: namespaces}); err != nil {
		dlog.Errorf(c, "error posting search paths %v and namespaces %v to root daemon: %v", paths, namespaces, err)
	}
	dlog.Debug(c, "search paths posted successfully")
}

func (s *session) Epilog(ctx context.Context) {
	if s.rootDaemon != nil {
		_, _ = s.rootDaemon.Disconnect(ctx, &empty.Empty{})
	}
	_ = s.pfDialer.Close()
	dlog.Info(ctx, "-- Session ended")
	close(s.done)
}

func (s *session) StartServices(g *dgroup.Group) {
	g.Go("remain", s.remainLoop)
	g.Go("intercept-port-forward", s.watchInterceptsHandler)
	g.Go("dial-request-watcher", s.dialRequestWatcher)
}

func runWithRetry(ctx context.Context, f func(context.Context) error) error {
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		if err := f(ctx); err != nil {
			dlog.Error(ctx, err)
			dtime.SleepWithContext(ctx, backoff)
			backoff *= 2
			if backoff > 3*time.Second {
				backoff = 3 * time.Second
			}
		}
	}
	return nil
}

func (s *session) Done() <-chan struct{} {
	return s.done
}

func (s *session) SessionInfo() *manager.SessionInfo {
	return s.sessionInfo
}

func (s *session) ApplyConfig(ctx context.Context) error {
	cfg, err := client.LoadConfig(ctx)
	if err != nil {
		return err
	}
	err = client.MergeAndReplace(ctx, s.sessionConfig, cfg, false)
	if err != nil {
		return err
	}
	if len(s.MappedNamespaces) == 0 {
		mns := client.GetConfig(ctx).Cluster().MappedNamespaces
		if len(mns) > 0 {
			s.SetMappedNamespaces(ctx, mns)
		}
	}
	return err
}

// getInfosForWorkloads returns a list of workloads found in the given namespace that fulfils the given filter criteria.
func (s *session) getInfosForWorkloads(
	ctx context.Context,
	namespaces []string,
	iMap map[string][]*manager.InterceptInfo,
	sMap map[string]*rpc.WorkloadInfo_Sidecar,
	filter rpc.ListRequest_Filter,
) []*rpc.WorkloadInfo {
	wiMap := make(map[types.UID]*rpc.WorkloadInfo)
	s.wlWatcher.eachService(ctx, s.GetManagerNamespace(), namespaces, func(svc *core.Service) {
		wls, err := s.wlWatcher.findMatchingWorkloads(ctx, svc)
		if err != nil {
			return
		}
		for _, workload := range wls {
			serviceUID := string(svc.UID)

			if wlInfo, ok := wiMap[workload.GetUID()]; ok {
				if _, ok := wlInfo.Services[serviceUID]; !ok {
					wlInfo.Services[serviceUID] = &rpc.WorkloadInfo_ServiceReference{
						Name:      svc.Name,
						Namespace: svc.Namespace,
						Ports:     getServicePorts(svc),
					}
				}
				continue
			}

			name := workload.GetName()
			dlog.Debugf(ctx, "Getting info for %s %s.%s, matching service %s.%s", workload.GetKind(), name, workload.GetNamespace(), svc.Name, svc.Namespace)

			wlInfo := &rpc.WorkloadInfo{
				Name:                 name,
				Namespace:            workload.GetNamespace(),
				WorkloadResourceType: workload.GetKind(),
				Uid:                  string(workload.GetUID()),
				Services: map[string]*rpc.WorkloadInfo_ServiceReference{
					string(svc.UID): {
						Name:      svc.Name,
						Namespace: svc.Namespace,
						Ports:     getServicePorts(svc),
					},
				},
			}
			var ok bool
			if wlInfo.InterceptInfos, ok = iMap[name]; !ok && filter <= rpc.ListRequest_INTERCEPTS {
				continue
			}
			if wlInfo.Sidecar, ok = sMap[name]; !ok && filter <= rpc.ListRequest_INSTALLED_AGENTS {
				continue
			}
			wiMap[workload.GetUID()] = wlInfo
		}
	})
	wiz := make([]*rpc.WorkloadInfo, len(wiMap))
	i := 0
	for _, wi := range wiMap {
		wiz[i] = wi
		i++
	}
	sort.Slice(wiz, func(i, j int) bool { return wiz[i].Name < wiz[j].Name })
	return wiz
}

func getServicePorts(svc *core.Service) []*rpc.WorkloadInfo_ServiceReference_Port {
	ports := make([]*rpc.WorkloadInfo_ServiceReference_Port, len(svc.Spec.Ports))
	for i, p := range svc.Spec.Ports {
		ports[i] = &rpc.WorkloadInfo_ServiceReference_Port{
			Name: p.Name,
			Port: p.Port,
		}
	}
	return ports
}

func (s *session) waitForSync(ctx context.Context) {
	s.wlWatcher.setNamespacesToWatch(ctx, s.GetCurrentNamespaces(true))
	s.wlWatcher.waitForSync(ctx)
}

func (s *session) WatchWorkloads(c context.Context, wr *rpc.WatchWorkloadsRequest, stream userd.WatchWorkloadsStream) error {
	s.waitForSync(c)
	s.ensureWatchers(c, wr.Namespaces)
	sCtx, sCancel := context.WithCancel(c)
	// We need to make sure the subscription ends when we leave this method, since this is the one consuming the snapshotAvailable channel.
	// Otherwise, the goroutine that writes to the channel will leak.
	defer sCancel()
	snapshotAvailable := s.wlWatcher.subscribe(sCtx)
	for {
		select {
		case <-c.Done(): // if context is done (usually the session's context).
			return nil
		case <-stream.Context().Done(): // if stream context is done.
			return nil
		case <-snapshotAvailable:
			snapshot, err := s.workloadInfoSnapshot(c, wr.GetNamespaces(), rpc.ListRequest_INTERCEPTABLE)
			if err != nil {
				return status.Errorf(codes.Unavailable, "failed to create WorkloadInfoSnapshot: %v", err)
			}
			if err := stream.Send(snapshot); err != nil {
				dlog.Errorf(c, "WatchWorkloads.Send() failed: %v", err)
				return err
			}
		}
	}
}

func (s *session) WorkloadInfoSnapshot(
	ctx context.Context,
	namespaces []string,
	filter rpc.ListRequest_Filter,
) (*rpc.WorkloadInfoSnapshot, error) {
	s.waitForSync(ctx)
	return s.workloadInfoSnapshot(ctx, namespaces, filter)
}

func (s *session) ensureWatchers(ctx context.Context,
	namespaces []string,
) {
	dlog.Debugf(ctx, "Ensure watchers %v", namespaces)
	wg := sync.WaitGroup{}
	wg.Add(len(namespaces))
	for _, ns := range namespaces {
		if ns == "" {
			// Don't use tm.ActualNamespace here because the accessibility of the namespace
			// is actually determined once the watcher starts
			ns = s.Namespace
		}
		wgp := &wg
		s.wlWatcher.ensureStarted(ctx, ns, func(started bool) {
			if started {
				dlog.Debugf(ctx, "watchers for %s started", ns)
			}
			if wgp != nil {
				wgp.Done()
				wgp = nil
			}
		})
	}
	wg.Wait()
}

func (s *session) workloadInfoSnapshot(
	ctx context.Context,
	namespaces []string,
	filter rpc.ListRequest_Filter,
) (*rpc.WorkloadInfoSnapshot, error) {
	is := s.getCurrentIntercepts()
	s.ensureWatchers(ctx, namespaces)

	var nss []string
	if filter == rpc.ListRequest_INTERCEPTS {
		// Special case, we don't care about namespaces in general. Instead, we use the intercepted namespaces
		if s.Namespace != "" {
			nss = []string{s.Namespace}
		}
		if len(nss) == 0 {
			// No active intercepts
			return &rpc.WorkloadInfoSnapshot{}, nil
		}
	} else {
		nss = make([]string, 0, len(namespaces))
		for _, ns := range namespaces {
			ns = s.ActualNamespace(ns)
			if ns != "" {
				nss = append(nss, ns)
			}
		}
	}
	if len(nss) == 0 {
		// none of the namespaces are currently mapped
		return &rpc.WorkloadInfoSnapshot{}, nil
	}

	iMap := make(map[string][]*manager.InterceptInfo, len(is))
nextIs:
	for _, i := range is {
		for _, ns := range nss {
			if i.Spec.Namespace == ns {
				iMap[i.Spec.Agent] = append(iMap[i.Spec.Agent], i.InterceptInfo)
				continue nextIs
			}
		}
	}

	sMap := make(map[string]*rpc.WorkloadInfo_Sidecar)
	for _, ns := range nss {
		for k, v := range s.getCurrentSidecarsInNamespace(ctx, ns) {
			data, err := json.Marshal(v)
			if err != nil {
				continue
			}
			sMap[k] = &rpc.WorkloadInfo_Sidecar{Json: data}
		}
	}

	workloadInfos := s.getInfosForWorkloads(ctx, nss, iMap, sMap, filter)
	return &rpc.WorkloadInfoSnapshot{Workloads: workloadInfos}, nil
}

var ErrSessionExpired = errors.New("session expired")

func (s *session) remainLoop(c context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer func() {
		ticker.Stop()
		c = dcontext.WithoutCancel(c)
		c, cancel := context.WithTimeout(c, 3*time.Second)
		defer cancel()
		if _, err := s.managerClient.Depart(c, s.SessionInfo()); err != nil {
			dlog.Errorf(c, "failed to depart from manager: %v", err)
		} else {
			// Depart succeeded so the traffic-manager has dropped the session. We should too
			if err = DeleteSessionInfoFromUserCache(c, s.daemonID); err != nil {
				dlog.Errorf(c, "failed to delete session from user cache: %v", err)
			}
		}
		s.managerConn.Close()
	}()

	for {
		select {
		case <-c.Done():
			return nil
		case <-ticker.C:
			if err := s.Remain(c); err != nil {
				return err
			}
		}
	}
}

func (s *session) UpdateStatus(c context.Context, cr *rpc.ConnectRequest) *rpc.ConnectInfo {
	config, err := client.DaemonKubeconfig(c, cr)
	if err != nil {
		return connectError(rpc.ConnectInfo_CLUSTER_FAILED, err)
	}

	if !cr.IsPodDaemon {
		envEQ := true
		for k, v := range cr.Environment {
			if k[0] == '-' {
				if _, ok := os.LookupEnv(k[:1]); ok {
					envEQ = false
					break
				}
			} else {
				if ov, ok := os.LookupEnv(k); !ok || ov != v {
					envEQ = false
					break
				}
			}
		}
		if !(envEQ && s.Kubeconfig.ContextServiceAndFlagsEqual(config)) {
			return &rpc.ConnectInfo{
				Error:            rpc.ConnectInfo_MUST_RESTART,
				ClusterContext:   s.Kubeconfig.Context,
				ClusterServer:    s.Kubeconfig.Server,
				ClusterId:        s.GetClusterId(c),
				ManagerInstallId: s.GetManagerInstallId(c),
			}
		}
	}

	namespaces := cr.MappedNamespaces
	if len(namespaces) == 1 && namespaces[0] == "all" {
		namespaces = nil
	}
	if len(namespaces) == 0 {
		namespaces = client.GetConfig(c).Cluster().MappedNamespaces
	}

	if s.SetMappedNamespaces(c, namespaces) {
		if len(namespaces) == 0 && k8sclient.CanWatchNamespaces(c) {
			s.StartNamespaceWatcher(c)
		}
		s.currentInterceptsLock.Lock()
		s.ingressInfo = nil
		s.currentInterceptsLock.Unlock()
	}
	s.subnetViaWorkloads = cr.SubnetViaWorkloads
	return s.Status(c)
}

func (s *session) Status(c context.Context) *rpc.ConnectInfo {
	cfg := s.Kubeconfig
	ret := &rpc.ConnectInfo{
		Error:              rpc.ConnectInfo_ALREADY_CONNECTED,
		ClusterContext:     cfg.Context,
		ClusterServer:      cfg.Server,
		ClusterId:          s.GetClusterId(c),
		ManagerInstallId:   s.GetManagerInstallId(c),
		SessionInfo:        s.SessionInfo(),
		ConnectionName:     s.daemonID.Name,
		KubeFlags:          s.OriginalFlagMap,
		Namespace:          s.Namespace,
		Intercepts:         &manager.InterceptInfoSnapshot{Intercepts: s.getCurrentInterceptInfos()},
		SubnetViaWorkloads: s.subnetViaWorkloads,
		Version: &common.VersionInfo{
			ApiVersion: client.APIVersion,
			Version:    client.Version(),
			Executable: client.GetExe(c),
			Name:       client.DisplayName,
		},
		ManagerNamespace: cfg.GetManagerNamespace(),
	}
	if len(s.MappedNamespaces) > 0 || len(s.sessionConfig.Cluster().MappedNamespaces) > 0 {
		ret.MappedNamespaces = s.GetCurrentNamespaces(true)
	}
	if s.rootDaemon != nil {
		var err error
		ret.DaemonStatus, err = s.rootDaemon.Status(c, &empty.Empty{})
		if err != nil {
			return connectError(rpc.ConnectInfo_DAEMON_FAILED, err)
		}
	}
	return ret
}

// Uninstall parts or all of Telepresence from the cluster if the client has sufficient credentials to do so.
//
// Uninstalling everything requires that the client owns the helm chart installation and has permissions to run
// a `helm uninstall traffic-manager`.
//
// Uninstalling all or specific agents require that the client can get and update the agents ConfigMap.
func (s *session) Uninstall(ctx context.Context, ur *rpc.UninstallRequest) (*common.Result, error) {
	api := k8sapi.GetK8sInterface(ctx).CoreV1()
	loadAgentConfigMap := func(ns string) (*core.ConfigMap, error) {
		cm, err := api.ConfigMaps(ns).Get(ctx, agentconfig.ConfigMap, meta.GetOptions{})
		if err != nil {
			if k8serrors.IsNotFound(err) {
				// there are no agents to remove
				return nil, nil
			}
			// TODO: find out if this is due to lack of access credentials and if so, report using errcat.User with more meaningful message
			return nil, err
		}
		return cm, nil
	}

	updateAgentConfigMap := func(ns string, cm *core.ConfigMap) error {
		_, err := api.ConfigMaps(ns).Update(ctx, cm, meta.UpdateOptions{})
		return err
	}

	// Removal of agents requested. We need the agents ConfigMap in order to do that.
	// This removal is deliberately done in the client instead of the traffic-manager so that RBAC can be configured
	// to prevent the clients from doing it.
	if ur.UninstallType == rpc.UninstallRequest_NAMED_AGENTS {
		// must have a valid namespace in order to uninstall named agents
		s.waitForSync(ctx)
		if ur.Namespace == "" {
			ur.Namespace = s.Namespace
		}
		s.wlWatcher.ensureStarted(ctx, ur.Namespace, nil)
		namespace := s.ActualNamespace(ur.Namespace)
		if namespace == "" {
			// namespace is not mapped
			return errcat.ToResult(errcat.User.Newf("namespace %s is not mapped", ur.Namespace)), nil
		}
		cm, err := loadAgentConfigMap(namespace)
		if err != nil || cm == nil {
			return errcat.ToResult(err), nil
		}
		changed := false
		ics := s.getCurrentIntercepts()
		for _, an := range ur.Agents {
			for _, ic := range ics {
				if ic.Spec.Namespace == namespace && ic.Spec.Agent == an {
					_ = s.removeIntercept(ctx, ic)
					break
				}
			}
			if _, ok := cm.Data[an]; ok {
				delete(cm.Data, an)
				changed = true
			}
		}
		if changed {
			return errcat.ToResult(updateAgentConfigMap(namespace, cm)), nil
		}
		return errcat.ToResult(nil), nil
	}
	if ur.UninstallType != rpc.UninstallRequest_ALL_AGENTS {
		return nil, status.Error(codes.InvalidArgument, "invalid uninstall request")
	}

	_ = s.ClearIntercepts(ctx)
	clearAgentsConfigMap := func(ns string) error {
		cm, err := loadAgentConfigMap(ns)
		if err != nil {
			return err
		}
		if cm == nil {
			return nil
		}
		if len(cm.Data) > 0 {
			cm.Data = nil
			return updateAgentConfigMap(ns, cm)
		}
		return nil
	}

	if ur.Namespace != "" {
		s.waitForSync(ctx)
		if ur.Namespace == "" {
			ur.Namespace = s.Namespace
		}
		s.wlWatcher.ensureStarted(ctx, ur.Namespace, nil)
		namespace := s.ActualNamespace(ur.Namespace)
		if namespace == "" {
			// namespace is not mapped
			return errcat.ToResult(errcat.User.Newf("namespace %s is not mapped", ur.Namespace)), nil
		}
		return errcat.ToResult(clearAgentsConfigMap(namespace)), nil
	} else {
		// Load all effected configmaps
		for _, ns := range s.GetCurrentNamespaces(true) {
			err := clearAgentsConfigMap(ns)
			if err != nil {
				return errcat.ToResult(err), nil
			}
		}
	}
	return errcat.ToResult(nil), nil
}

func (s *session) getOutboundInfo(ctx context.Context, cr *rpc.ConnectRequest) *rootdRpc.OutboundInfo {
	// We'll figure out the IP address of the API server(s) so that we can tell the daemon never to proxy them.
	// This is because in some setups the API server will be in the same CIDR range as the pods, and the
	// daemon will attempt to proxy traffic to it. This usually results in a loss of all traffic to/from
	// the cluster, since an open tunnel to the traffic-manager (via the API server) is itself required
	// to communicate with the cluster.
	neverProxy := make([]*manager.IPNet, 0, 1+len(s.NeverProxy))
	serverURL, err := url.Parse(s.Server)
	if err != nil {
		// This really shouldn't happen as we are connected to the server
		dlog.Errorf(ctx, "Unable to parse url for k8s server %s: %v", s.Server, err)
	} else {
		hostname := serverURL.Hostname()
		rawIP := iputil.Parse(hostname)
		ips := []net.IP{rawIP}
		if rawIP == nil {
			var err error
			ips, err = net.LookupIP(hostname)
			if err != nil {
				dlog.Errorf(ctx, "Unable to do DNS lookup for k8s server %s: %v", hostname, err)
				ips = []net.IP{}
			}
		}
		for _, ip := range ips {
			mask := net.CIDRMask(128, 128)
			if ipv4 := ip.To4(); ipv4 != nil {
				mask = net.CIDRMask(32, 32)
				ip = ipv4
			}
			if !ip.IsLoopback() {
				ipnet := &net.IPNet{IP: ip, Mask: mask}
				neverProxy = append(neverProxy, iputil.IPNetToRPC(ipnet))
			}
		}
	}
	for _, np := range s.NeverProxy {
		neverProxy = append(neverProxy, iputil.IPNetToRPC((*net.IPNet)(np)))
	}
	info := &rootdRpc.OutboundInfo{
		Session:            s.sessionInfo,
		NeverProxySubnets:  neverProxy,
		HomeDir:            homedir.HomeDir(),
		Namespace:          s.Namespace,
		ManagerNamespace:   s.GetManagerNamespace(),
		SubnetViaWorkloads: s.subnetViaWorkloads,
		KubeFlags:          cr.KubeFlags,
		KubeconfigData:     cr.KubeconfigData,
	}

	if s.DNS != nil {
		info.Dns = &rootdRpc.DNSConfig{
			ExcludeSuffixes: s.DNS.ExcludeSuffixes,
			IncludeSuffixes: s.DNS.IncludeSuffixes,
			Excludes:        s.DNS.Excludes,
			Mappings:        s.DNS.Mappings.ToRPC(),
			LookupTimeout:   durationpb.New(s.DNS.LookupTimeout.Duration),
		}
		if len(s.DNS.LocalIP) > 0 {
			info.Dns.LocalIp = s.DNS.LocalIP.IP()
		}
		if len(s.DNS.RemoteIP) > 0 {
			info.Dns.RemoteIp = s.DNS.RemoteIP.IP()
		}
	}

	if len(s.AlsoProxy) > 0 {
		info.AlsoProxySubnets = make([]*manager.IPNet, len(s.AlsoProxy))
		for i, ap := range s.AlsoProxy {
			info.AlsoProxySubnets[i] = iputil.IPNetToRPC((*net.IPNet)(ap))
		}
	}
	if len(s.AllowConflictingSubnets) > 0 {
		info.AllowConflictingSubnets = make([]*manager.IPNet, len(s.AllowConflictingSubnets))
		for i, ap := range s.AllowConflictingSubnets {
			info.AllowConflictingSubnets[i] = iputil.IPNetToRPC((*net.IPNet)(ap))
		}
	}
	return info
}

func (s *session) connectRootDaemon(ctx context.Context, oi *rootdRpc.OutboundInfo) (rd rootdRpc.DaemonClient, err error) {
	// establish a connection to the root daemon gRPC grpcService
	dlog.Info(ctx, "Connecting to root daemon...")
	svc := userd.GetService(ctx)
	if svc.RootSessionInProcess() {
		// Just run the root session in-process.
		rootSession, err := rootd.NewInProcSession(ctx, oi, s.managerClient, s.managerVersion)
		if err != nil {
			return nil, err
		}
		if err = rootSession.Start(ctx, dgroup.NewGroup(ctx, dgroup.GroupConfig{})); err != nil {
			return nil, err
		}
		rd = rootSession
	} else {
		var conn *grpc.ClientConn
		conn, err = socket.Dial(ctx, socket.RootDaemonPath(ctx),
			grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		)
		if err != nil {
			return nil, fmt.Errorf("unable open root daemon socket: %w", err)
		}
		defer func() {
			if err != nil {
				conn.Close()
			}
		}()
		rd = rootdRpc.NewDaemonClient(conn)

		for attempt := 1; ; attempt++ {
			var rootStatus *rootdRpc.DaemonStatus
			if rootStatus, err = rd.Connect(ctx, oi); err != nil {
				return nil, fmt.Errorf("failed to connect to root daemon: %w", err)
			}
			oc := rootStatus.OutboundConfig
			if oc == nil || oc.Session == nil {
				// This is an internal error. Something is wrong with the root daemon.
				return nil, errors.New("root daemon's OutboundConfig has no Session")
			}
			if oc.Session.SessionId == oi.Session.SessionId {
				break
			}

			// Root daemon was running an old session. This indicates that this daemon somehow
			// crashed without disconnecting. So let's do that now, and then reconnect...
			if attempt == 2 {
				// ...or not, since we've already done it.
				return nil, errors.New("unable to reconnect to root daemon")
			}
			if _, err = rd.Disconnect(ctx, &empty.Empty{}); err != nil {
				return nil, fmt.Errorf("failed to disconnect from the root daemon: %w", err)
			}
		}
	}

	// The root daemon needs time to set up the TUN-device and DNS, which involves interacting
	// with the cluster-side traffic-manager. We know that the traffic-manager is up and
	// responding at this point, so it shouldn't take too long.
	ctx, cancel := client.GetConfig(ctx).Timeouts().TimeoutContext(ctx, client.TimeoutTrafficManagerAPI)
	defer cancel()
	if _, err = rd.WaitForNetwork(ctx, &empty.Empty{}); err != nil {
		if se, ok := status.FromError(err); ok {
			err = se.Err()
		}
		return nil, fmt.Errorf("failed to connect to root daemon: %v", err)
	}
	dlog.Debug(ctx, "Connected to root daemon")
	return rd, nil
}
