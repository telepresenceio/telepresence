package cluster

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"regexp"
	"slices"
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
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

const (
	supportedKubeAPIVersion = "1.17.0"
	agentContainerName      = "traffic-agent"
	managerAppName          = "traffic-manager"
)

type Info interface {
	// Watch changes of an ClusterInfo and write them on the given stream
	Watch(context.Context, rpc.Manager_WatchClusterInfoServer) error

	// ID of the installed ns
	ID() string

	// clusterID of the cluster
	ClusterID() string

	// SetAdditionalAlsoProxy assigns a slice that will be added to the Routing.AlsoProxySubnets slice
	// when notifications are sent.
	SetAdditionalAlsoProxy(ctx context.Context, subnets []*rpc.IPNet)
}

type subnetRetriever interface {
	changeNotifier(ctx context.Context, updateSubnets func(subnet.Set))
	viable(ctx context.Context) bool
}

type info struct {
	rpc.ClusterInfo
	ciSubs *clusterInfoSubscribers

	// addAlsoProxy are extra subnets that will be added to the also-proxy slice
	// when sending notifications to the client.
	addAlsoProxy []*rpc.IPNet

	// installID is the UID of the manager's namespace
	installID string

	// clusterID is the UID of the default namespace
	clusterID string
}

const IDZero = "00000000-0000-0000-0000-000000000000"

func NewInfo(ctx context.Context) Info {
	env := managerutil.GetEnv(ctx)
	managedNamespaces := env.ManagedNamespaces
	namespaced := len(managedNamespaces) > 0
	oi := info{}
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
	if oi.installID, err = GetInstallIDFunc(ctx, client, env.ManagerNamespace); err != nil {
		// We use a default clusterID because we don't want to fail if
		// the traffic-manager doesn't have the ability to get the namespace
		oi.installID = IDZero
		dlog.Warnf(ctx, "unable to get namespace \"%s\", will use default installID: %s: %v",
			env.ManagerNamespace, oi.installID, err)
	}

	// backwards compat
	// TODO delete after default ns licenses expire
	if oi.clusterID, err = GetInstallIDFunc(ctx, client, "default"); err != nil {
		// We use a default clusterID because we don't want to fail if
		// the traffic-manager doesn't have the ability to get the namespace
		oi.clusterID = IDZero
		dlog.Infof(ctx,
			"unable to get namespace \"default\", but it is only necessary for compatibility with old licesnses: %v", err)
	}

	dummyIP := "1.1.1.1"
	oi.InjectorSvcIp, oi.InjectorSvcPort, err = getInjectorSvcIP(ctx, env, client)
	if err != nil {
		dlog.Warn(ctx, err)
	} else if len(oi.InjectorSvcIp) == 16 {
		// Must use an IPv6 IP to get the correct error message.
		dummyIP = "1:1::1"
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
			ClusterIP: dummyIP,
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
	if oi.ServiceSubnet == nil && len(oi.InjectorSvcIp) > 0 {
		// Using a "kubectl cluster-info dump" or scanning all services generates a lot of unwanted traffic
		// and would quite possibly also require elevated permissions, so instead, we derive the service subnet
		// from the agent-injector service IP (the traffic-manager has clusterIP=None). This is cheating but
		// a cluster may only have one service subnet and the mask is unlikely to cover less than half the bits.
		ip := net.IP(oi.InjectorSvcIp)
		dlog.Infof(ctx, "Deriving serviceSubnet from %s (the IP of agent-injector.%s)", ip, env.ManagerNamespace)
		bits := len(ip) * 8
		ones := bits / 2
		mask := net.CIDRMask(ones, bits) // will yield a 16 bit mask on IPv4 and 64 bit mask on IPv6.
		oi.ServiceSubnet = &rpc.IPNet{Ip: ip.Mask(mask), Mask: int32(ones)}
	}

	podCIDRStrategy := env.PodCIDRStrategy
	dlog.Infof(ctx, "Using podCIDRStrategy: %s", podCIDRStrategy)

	oi.ManagerPodIp = env.PodIP
	oi.ManagerPodPort = int32(env.ServerPort)
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
			if namespaced || !oi.watchNodeSubnets(ctx, false) {
				oi.watchPodSubnets(ctx, managedNamespaces)
			}
		}()
	case strings.EqualFold("nodePodCIDRs", podCIDRStrategy):
		if namespaced {
			dlog.Errorf(ctx, "cannot use POD_CIDR_STRATEGY %q with a namespaced traffic-manager", podCIDRStrategy)
		} else {
			go oi.watchNodeSubnets(ctx, true)
		}
	case strings.EqualFold("coverPodIPs", podCIDRStrategy):
		go oi.watchPodSubnets(ctx, managedNamespaces)
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

func (oi *info) watchPodSubnets(ctx context.Context, namespaces []string) {
	retriever := newPodWatcher(ctx, namespaces)
	if !retriever.viable(ctx) {
		dlog.Errorf(ctx, "Unable to derive subnets from IPs of pods")
		return
	}
	dlog.Infof(ctx, "Deriving subnets from IPs of pods")
	oi.watchSubnets(ctx, retriever)
}

func (oi *info) setSubnetsFromEnv(ctx context.Context) bool {
	subnets := managerutil.GetEnv(ctx).PodCIDRs
	if len(subnets) > 0 {
		oi.PodSubnets = subnetsToRPC(subnets)
		oi.ciSubs.notify(ctx, oi.clusterInfo())
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

// SetAdditionalAlsoProxy assigns a slice that will be added to the Routing.AlsoProxySubnets slice
// when notifications are sent.
func (oi *info) SetAdditionalAlsoProxy(ctx context.Context, subnets []*rpc.IPNet) {
	eq := func(a, b *rpc.IPNet) bool {
		return a.Mask == b.Mask && bytes.Equal(a.Ip, b.Ip)
	}
	if !slices.EqualFunc(oi.addAlsoProxy, subnets, eq) {
		oi.addAlsoProxy = subnets
		oi.ciSubs.notify(ctx, oi.clusterInfo())
	}
}

func (oi *info) ID() string {
	return oi.installID
}

func (oi *info) ClusterID() string {
	return oi.clusterID
}

func (oi *info) clusterInfo() *rpc.ClusterInfo {
	rt := oi.Routing
	if len(oi.addAlsoProxy) > 0 {
		aps := rt.AlsoProxySubnets
		cps := append(make([]*rpc.IPNet, 0, len(aps)+len(oi.addAlsoProxy)), aps...)
		for _, s := range oi.addAlsoProxy {
			if !slices.ContainsFunc(cps, func(a *rpc.IPNet) bool {
				return a.Mask == s.Mask && bytes.Equal(a.Ip, s.Ip)
			}) {
				cps = append(cps, s)
			}
		}
		rt = &rpc.Routing{
			AlsoProxySubnets:        cps,
			NeverProxySubnets:       rt.NeverProxySubnets,
			AllowConflictingSubnets: rt.AllowConflictingSubnets,
		}
	}

	ci := &rpc.ClusterInfo{
		ServiceSubnet:   oi.ServiceSubnet,
		PodSubnets:      make([]*rpc.IPNet, len(oi.PodSubnets)),
		ManagerPodIp:    oi.ManagerPodIp,
		ManagerPodPort:  oi.ManagerPodPort,
		InjectorSvcIp:   oi.InjectorSvcIp,
		InjectorSvcPort: oi.InjectorSvcPort,
		InjectorSvcHost: oi.InjectorSvcHost,
		Routing:         rt,
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
