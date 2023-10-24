package cluster

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/blang/semver"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/pfs"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
	"github.com/telepresenceio/telepresence/v2/pkg/watcher"
)

const (
	supportedKubeAPIVersion = "1.17.0"
	agentContainerName      = "traffic-agent"
	managerAppName          = "traffic-manager"
)

type Info interface {
	// Watch changes of an ClusterInfo and write them on the given stream
	Watch(context.Context, rpc.Manager_WatchClusterInfoServer) error

	// ID of the cluster
	ID() string

	// GetTrafficManagerPods acquires all pods that have `traffic-manager` in
	// their name
	GetTrafficManagerPods(context.Context) ([]*corev1.Pod, error)

	// GetTrafficAgentPods acquires all pods that have a `traffic-agent`
	// container in their spec
	GetTrafficAgentPods(context.Context, string) ([]*corev1.Pod, error)
}

type subnetRetriever interface {
	changeNotifier(ctx context.Context, updateSubnets func(subnet.Set))
	viable(ctx context.Context) bool
}

type info struct {
	rpc.ClusterInfo
	ciSubs   *clusterInfoSubscribers
	podState watcher.State[iputil.IPKey, *pfs.Pod]

	// clusterID is the UID of the default namespace
	clusterID string
}

const IDZero = "00000000-0000-0000-0000-000000000000"

func NewInfo(ctx context.Context, podState watcher.State[iputil.IPKey, *pfs.Pod]) Info {
	env := managerutil.GetEnv(ctx)
	managedNamespaces := env.ManagedNamespaces
	namespaced := len(managedNamespaces) > 0
	oi := info{
		podState: podState,
	}
	ki := k8sapi.GetK8sInterface(ctx)

	// Validate that the kubernetes server version is supported
	dc := ki.Discovery()
	info, err := dc.ServerVersion()
	if err != nil {
		dlog.Errorf(ctx, "error getting server information: %s", err)
	} else {
		gitVer, err := semver.Parse(strings.TrimPrefix(info.GitVersion, "v"))
		if err != nil {
			dlog.Errorf(ctx, "error converting version %s to semver: %s", info.GitVersion, err)
		}
		supGitVer, err := semver.Parse(supportedKubeAPIVersion)
		if err != nil {
			dlog.Errorf(ctx, "error converting known version %s to semver: %s", supportedKubeAPIVersion, err)
		}
		if gitVer.LT(supGitVer) {
			dlog.Errorf(ctx,
				"kubernetes server versions older than %s are not supported, using %s .",
				supportedKubeAPIVersion, info.GitVersion)
		}
	}

	client := ki.CoreV1()
	if oi.clusterID, err = GetIDFunc(ctx, client, env.ManagerNamespace); err != nil {
		// We use a default clusterID because we don't want to fail if
		// the traffic-manager doesn't have the ability to get the namespace
		oi.clusterID = IDZero
		dlog.Warnf(ctx, "unable to get namespace \"default\", will use default clusterID: %s: %v",
			oi.clusterID, err)
	}

	// make an attempt to create a service with ClusterIP that is out of range and then
	// check the error message for the correct range as suggested tin the second answer here:
	//   https://stackoverflow.com/questions/44190607/how-do-you-find-the-cluster-service-cidr-of-a-kubernetes-cluster
	// This requires an additional permission to create a service, which the traffic-manager
	// should have.
	svc := corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind: "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: env.ManagerNamespace,
			Name:      "t2-tst-dummy",
		},
		Spec: corev1.ServiceSpec{
			Ports:     []corev1.ServicePort{{Port: 443}},
			ClusterIP: "1.1.1.1",
		},
	}
	if _, err = client.Services(env.ManagerNamespace).Create(ctx, &svc, metav1.CreateOptions{}); err != nil {
		svcCIDRrx := regexp.MustCompile(`range of valid IPs is (.*)$`)
		if match := svcCIDRrx.FindStringSubmatch(err.Error()); match != nil {
			var cidr *net.IPNet
			if _, cidr, err = net.ParseCIDR(match[1]); err != nil {
				dlog.Errorf(ctx, "unable to parse service CIDR %q", match[1])
			} else {
				dlog.Infof(ctx, "Extracting service subnet %v from create service error message", cidr)
				oi.ServiceSubnet = iputil.IPNetToRPC(cidr)
			}
		} else {
			dlog.Errorf(ctx, "unable to extract service subnet from error message %q", err.Error())
		}
	}

	if err != nil {
		dlog.Warn(ctx, err)
	}
	if oi.ServiceSubnet == nil {
		// Using a "kubectl cluster-info dump" or scanning all services generates a lot of unwanted traffic
		// and would quite possibly also require elevated permissions, so instead, we derive the service subnet
		// from the traffic-manager service IP. This is cheating but a cluster may only have one service subnet
		// and the mask is unlikely to cover less than half the bits.
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", "traffic-manager")
		if err != nil || len(ips) == 0 {
			dlog.Warn(ctx, "traffic manager is not able to resolve the IP of its own service")
		} else {
			ip := ips[0]
			dlog.Infof(ctx, "Deriving serviceSubnet from %s (the IP of traffic-manager.%s)", ip, env.ManagerNamespace)
			bits := len(ip) * 8
			ones := bits / 2
			mask := net.CIDRMask(ones, bits) // will yield a 16 bit mask on IPv4 and 64 bit mask on IPv6.
			oi.ServiceSubnet = &rpc.IPNet{Ip: ip.Mask(mask), Mask: int32(ones)}
		}
	}

	podCIDRStrategy := env.PodCIDRStrategy
	dlog.Infof(ctx, "Using podCIDRStrategy: %s", podCIDRStrategy)

	oi.ManagerPodIp = env.PodIP
	oi.ManagerPodPort = int32(env.ServerPort)
	oi.InjectorSvcIp, oi.InjectorSvcPort, err = getInjectorSvcIP(ctx, env, client)
	if err != nil {
		dlog.Warnf(ctx, "failed to detect injector service ClusterIP; service connectivity check will be disabled in clients: %s", err)
	}
	oi.InjectorSvcHost = fmt.Sprintf("%s.%s", env.AgentInjectorName, env.ManagerNamespace)

	alsoProxy := env.ClientRoutingAlsoProxySubnets
	neverProxy := env.ClientRoutingNeverProxySubnets
	allowConflicting := env.ClientRoutingAllowConflictingSubnets
	dlog.Infof(ctx, "Using AlsoProxy: %v", alsoProxy)
	dlog.Infof(ctx, "Using NeverProxy: %v", neverProxy)
	dlog.Infof(ctx, "Using AllowConflicting: %v", allowConflicting)

	oi.Routing = &rpc.Routing{
		AlsoProxySubnets:        make([]*rpc.IPNet, len(alsoProxy)),
		NeverProxySubnets:       make([]*rpc.IPNet, len(neverProxy)),
		AllowConflictingSubnets: make([]*rpc.IPNet, len(allowConflicting)),
	}
	for i, sn := range alsoProxy {
		oi.Routing.AlsoProxySubnets[i] = iputil.IPNetToRPC(sn)
	}
	for i, sn := range neverProxy {
		oi.Routing.NeverProxySubnets[i] = iputil.IPNetToRPC(sn)
	}

	for i, sn := range allowConflicting {
		oi.Routing.AllowConflictingSubnets[i] = iputil.IPNetToRPC(sn)
	}

	clusterDomain := getClusterDomain(ctx, oi.InjectorSvcIp, env)
	dlog.Infof(ctx, "Using cluster domain %q", clusterDomain)
	oi.Dns = &rpc.DNS{
		IncludeSuffixes: env.ClientDnsIncludeSuffixes,
		ExcludeSuffixes: env.ClientDnsExcludeSuffixes,
		KubeIp:          env.PodIP,
		ClusterDomain:   clusterDomain,
	}

	dlog.Infof(ctx, "ExcludeSuffixes: %+v", oi.Dns.ExcludeSuffixes)
	dlog.Infof(ctx, "IncludeSuffixes: %+v", oi.Dns.IncludeSuffixes)

	oi.ciSubs = newClusterInfoSubscribers(oi.clusterInfo())

	switch {
	case strings.EqualFold("auto", podCIDRStrategy):
		go func() {
			if namespaced || oi.podState != nil || !oi.watchNodeSubnets(ctx, false) {
				oi.watchPodSubnets(ctx)
			}
		}()
	case strings.EqualFold("nodePodCIDRs", podCIDRStrategy):
		if namespaced {
			dlog.Errorf(ctx, "cannot use POD_CIDR_STRATEGY %q with a namespaced traffic-manager", podCIDRStrategy)
		} else {
			go oi.watchNodeSubnets(ctx, true)
		}
	case strings.EqualFold("coverPodIPs", podCIDRStrategy):
		go oi.watchPodSubnets(ctx)
	case strings.EqualFold("environment", podCIDRStrategy):
		oi.setSubnetsFromEnv(ctx)
	default:
		dlog.Errorf(ctx, "invalid POD_CIDR_STRATEGY %q", podCIDRStrategy)
	}
	return &oi
}

func getClusterDomain(ctx context.Context, svcIp net.IP, env *managerutil.Env) string {
	desiredMatch := env.AgentInjectorName + "." + env.ManagerNamespace + ".svc."
	addr := svcIp.String()

	for retry := 0; retry <= 2; retry++ {
		if retry > 0 {
			dlog.Debugf(ctx, "retry %d of reverse lookup of agent-injector", retry+1)
		}
		if names, err := net.LookupAddr(addr); err == nil {
			for _, name := range names {
				if strings.HasPrefix(name, desiredMatch) {
					dlog.Infof(ctx, `Cluster domain derived from agent-injector reverse lookup %q`, name)
					return name[len(desiredMatch):]
				}
			}
		}
		// If no reverse lookups are found containing the cluster domain, then that's probably because the
		// DNS for the service isn't completely setup yet.
		time.Sleep(300 * time.Millisecond)
	}
	dlog.Infof(ctx, `Unable to determine cluster domain from CNAME of %s"`, env.AgentInjectorName)
	return "cluster.local."
}

func (oi *info) watchNodeSubnets(ctx context.Context, mustSucceed bool) bool {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	informerFactory := informers.NewSharedInformerFactory(k8sapi.GetK8sInterface(ctx), 0)
	nodeController := informerFactory.Core().V1().Nodes()
	nodeLister := nodeController.Lister()
	nodeInformer := nodeController.Informer()

	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())

	retriever, err := newNodeWatcher(ctx, nodeLister, nodeInformer)
	if err != nil {
		if mustSucceed {
			dlog.Errorf(ctx, "failed to create node watcher: %v", err)
		}
		return false
	}
	if !retriever.viable(ctx) {
		if mustSucceed {
			dlog.Errorf(ctx, "Unable to derive subnets from nodes")
		}
		return false
	}
	dlog.Infof(ctx, "Deriving subnets from podCIRs of nodes")
	oi.watchSubnets(ctx, retriever)
	return true
}

func getInjectorSvcIP(ctx context.Context, env *managerutil.Env, client v1.CoreV1Interface) ([]byte, int32, error) {
	sc, err := client.Services(env.ManagerNamespace).Get(ctx, env.AgentInjectorName, metav1.GetOptions{})
	if err != nil {
		return nil, 0, err
	}
	p := int32(0)
	for _, port := range sc.Spec.Ports {
		if port.Name == "https" {
			p = port.Port
			break
		}
	}
	return iputil.Parse(sc.Spec.ClusterIP), p, nil
}

func (oi *info) watchPodSubnets(ctx context.Context) {
	// The time we wait from when the first change arrived until we actually do something. This
	// so that more changes can arrive (hopefully all of them) before everything is recalculated.
	dlog.Infof(ctx, "Deriving subnets from IPs of pods")
	const podCollectTime = 3 * time.Second
	var subnetSet subnet.Set
	oi.podState.AddChangeNotifier(func() {
		subnets := subnet.NewSet(pfs.PodSubnets(oi.podState))
		if !subnets.Equals(subnetSet) {
			subnetSet = subnets
			oi.PodSubnets = subnetSetToRPC(subnets)
			oi.ciSubs.notify(ctx, oi.clusterInfo())
		}
	}, podCollectTime)
}

func (oi *info) setSubnetsFromEnv(ctx context.Context) bool {
	subnets := managerutil.GetEnv(ctx).PodCIDRs
	if len(subnets) > 0 {
		oi.PodSubnets = subnetsToRPC(subnets)
		dlog.Infof(ctx, "Using subnets from POD_CIDRS environment variable")
		return true
	}
	return false
}

// Watch will start by sending an initial snapshot of the ClusterInfo on the given stream
// and then enter a loop where it waits for updates and sends new snapshots.
func (oi *info) Watch(ctx context.Context, oiStream rpc.Manager_WatchClusterInfoServer) error {
	return oi.ciSubs.subscriberLoop(ctx, oiStream)
}

func (oi *info) ID() string {
	return oi.clusterID
}

func (oi *info) clusterInfo() *rpc.ClusterInfo {
	ci := &rpc.ClusterInfo{
		ServiceSubnet:   oi.ServiceSubnet,
		PodSubnets:      make([]*rpc.IPNet, len(oi.PodSubnets)),
		ManagerPodIp:    oi.ManagerPodIp,
		ManagerPodPort:  oi.ManagerPodPort,
		InjectorSvcIp:   oi.InjectorSvcIp,
		InjectorSvcPort: oi.InjectorSvcPort,
		InjectorSvcHost: oi.InjectorSvcHost,
		Routing:         oi.Routing,
		Dns:             oi.Dns,
		KubeDnsIp:       oi.Dns.KubeIp,
		ClusterDomain:   oi.Dns.ClusterDomain,
	}
	copy(ci.PodSubnets, oi.PodSubnets)
	return ci
}

func (oi *info) watchSubnets(ctx context.Context, retriever subnetRetriever) {
	retriever.changeNotifier(ctx, func(subnets subnet.Set) {
		oi.PodSubnets = subnetSetToRPC(subnets)
		oi.ciSubs.notify(ctx, oi.clusterInfo())
	})
}

func subnetSetToRPC(cidrMap subnet.Set) []*rpc.IPNet {
	return subnetsToRPC(cidrMap.AppendSortedTo(nil))
}

func subnetsToRPC(subnets []*net.IPNet) []*rpc.IPNet {
	rpcSubnets := make([]*rpc.IPNet, len(subnets))
	for i, s := range subnets {
		rpcSubnets[i] = iputil.IPNetToRPC(s)
	}
	return rpcSubnets
}

// GetTrafficAgentPods gets all pods that have a `traffic-agent` container
// in them.
func (oi *info) GetTrafficAgentPods(ctx context.Context, agents string) ([]*corev1.Pod, error) {
	// We don't get agents if they explicitly say false
	if agents == "None" {
		return nil, nil
	}
	client := k8sapi.GetK8sInterface(ctx).CoreV1()
	podList, err := client.Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	// This is useful to determine how many pods we *should* be
	// getting logs for
	dlog.Debugf(ctx, "Found %d pod that contain a traffic-agent", len(podList.Items))

	var agentPods []*corev1.Pod
	for _, pod := range podList.Items {
		pod := pod
		if agents != "all" && !strings.Contains(pod.Name, agents) {
			continue
		}
		for _, container := range pod.Spec.Containers {
			if container.Name == agentContainerName {
				agentPods = append(agentPods, &pod)
				break
			}
		}
	}
	return agentPods, nil
}

// GetTrafficManagerPods gets all pods in the manager's namespace that have
// `traffic-manager` in the name.
func (oi *info) GetTrafficManagerPods(ctx context.Context) ([]*corev1.Pod, error) {
	client := k8sapi.GetK8sInterface(ctx).CoreV1()
	env := managerutil.GetEnv(ctx)
	podList, err := client.Pods(env.ManagerNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	// This is useful to determine how many pods we *should* be
	// getting logs for
	dlog.Debugf(ctx, "Found %d traffic-manager pods", len(podList.Items))

	var tmPods []*corev1.Pod
	for _, pod := range podList.Items {
		pod := pod
		if strings.Contains(pod.Name, managerAppName) {
			tmPods = append(tmPods, &pod)
		}
	}
	return tmPods, nil
}
