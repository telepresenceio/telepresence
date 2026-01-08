package cluster

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	auth "k8s.io/api/authorization/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedCore "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

const (
	supportedKubeAPIVersion = "1.28.0"
)

type Info interface {
	// Watch changes of an ClusterInfo and write them on the given stream
	Watch(context.Context, rpc.Manager_WatchClusterInfoServer) error

	// ID of the installed ns
	ID() string

	ServiceIP() net.IP

	// SetAdditionalAlsoProxy assigns a slice that will be added to the Routing.AlsoProxySubnets slice
	// when notifications are sent.
	SetAdditionalAlsoProxy(ctx context.Context, subnets []*rpc.IPNet)

	ClusterDomain() string
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
}

const IDZero = "00000000-0000-0000-0000-000000000000"

func NewInfo(ctx context.Context) (Info, error) {
	env := managerutil.GetEnv(ctx)
	oi := info{}
	ki := k8sapi.GetK8sInterface(ctx)

	// Validate that the kubernetes server version is supported
	dc := ki.Discovery()
	info, err := dc.ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("error getting Kubernetes server information: %w", err)
	}

	k8sVersion, err := semver.Parse(strings.TrimPrefix(info.GitVersion, "v"))
	if err != nil {
		return nil, fmt.Errorf("error parsing Kubernetes server information %q: %w", info.GitVersion, err)
	}

	clog.Infof(ctx, "Kubernetes server version %s", k8sVersion)
	supGitVer, err := semver.Parse(supportedKubeAPIVersion)
	if err != nil {
		clog.Errorf(ctx, "error converting known version %s to semver: %s", supportedKubeAPIVersion, err)
	}
	if k8sVersion.LT(supGitVer) {
		clog.Errorf(ctx,
			"kubernetes server versions older than %s might work OK but are not supported, using %s .",
			supportedKubeAPIVersion, k8sVersion)
	}

	client := ki.CoreV1()
	if oi.installID, err = GetInstallIDFunc(ctx, client, env.ManagerNamespace); err != nil {
		// Use a default installID because we don't want to fail if the traffic-manager can't get the namespace
		oi.installID = IDZero
		clog.Warnf(ctx, "unable to get namespace \"%s\", will use default installID: %s: %v",
			env.ManagerNamespace, oi.installID, err)
	}

	dummyIP := "1.1.1.1"
	if managerutil.AgentInjectorEnabled(ctx) {
		oi.InjectorSvcIp, oi.InjectorSvcPort, err = getInjectorSvcIP(ctx, env, client)
		if err != nil {
			clog.Warn(ctx, err)
		} else if len(oi.InjectorSvcIp) == 16 {
			// Must use an IPv6 IP to get the correct error message.
			dummyIP = "1:1::1"
		}
	}

	clog.Infof(ctx, "Enabled support for the following workload kinds: %v", env.EnabledWorkloadKinds)

	if k8sVersion.GE(semver.MustParse("1.33.0")) { // The ServiceCIDRs() API was introduced in version 1.33
		svcCIDRs, err := ki.NetworkingV1().ServiceCIDRs().List(ctx, meta.ListOptions{})
		if err != nil {
			clog.Errorf(ctx, "error listing service CIDRs: %v", err)
		} else {
			itms := svcCIDRs.Items
			clog.Debugf(ctx, "Found %d service CIDRs", len(itms))
			for _, itm := range itms {
				for _, cidr := range itm.Spec.CIDRs {
					clog.Infof(ctx, "Found service CIDR %s", cidr)
					pfx, err := netip.ParsePrefix(cidr)
					if err != nil {
						clog.Errorf(ctx, "error parsing service CIDR %s: %s", cidr, err)
						continue
					}
					sc, _ := pfx.MarshalBinary()
					oi.ServiceCidrs = append(oi.ServiceCidrs, sc)
				}
			}
		}
	} else {
		// make an attempt to create a service with ClusterIP that is out of range and then
		// check the error message for the correct range as suggested tin the second answer here:
		//   https://stackoverflow.com/questions/44190607/how-do-you-find-the-cluster-service-cidr-of-a-kubernetes-cluster
		// This requires an additional permission to create a service, which the traffic-manager
		// should have.
		svc := core.Service{
			TypeMeta: meta.TypeMeta{
				Kind: "Service",
			},
			ObjectMeta: meta.ObjectMeta{
				Namespace: env.ManagerNamespace,
				Name:      "t2-tst-dummy",
			},
			Spec: core.ServiceSpec{
				Ports:     []core.ServicePort{{Port: 443}},
				ClusterIP: dummyIP,
			},
		}
		if _, err = client.Services(env.ManagerNamespace).Create(ctx, &svc, meta.CreateOptions{}); err != nil {
			svcCIDRrx := regexp.MustCompile(`range of valid IPs is (.*)$`)
			if match := svcCIDRrx.FindStringSubmatch(err.Error()); match != nil {
				if pfx, err := netip.ParsePrefix(match[1]); err != nil {
					clog.Errorf(ctx, "unable to parse service CIDR %q", match[1])
				} else {
					clog.Infof(ctx, "Extracting service subnet %v from create service error message", pfx)
					sc, _ := pfx.MarshalBinary()
					oi.ServiceCidrs = [][]byte{sc}
				}
			} else {
				clog.Errorf(ctx, "unable to extract service subnet from error message %q", err.Error())
			}
		}
	}

	if len(oi.ServiceCidrs) == 0 && len(oi.InjectorSvcIp) > 0 {
		// Using a "kubectl cluster-info dump" or scanning all services generates a lot of unwanted traffic
		// and would quite possibly also require elevated permissions, so instead, we derive the service subnet
		// from the agent-injector service IP (the traffic-manager has clusterIP=None). This is cheating but
		// a cluster may only have one service subnet and the mask is unlikely to cover less than half the bits.
		ip, _ := netip.AddrFromSlice(oi.InjectorSvcIp)
		if ip.Is4In6() {
			ip = netip.AddrFrom4(ip.As4())
		}
		clog.Infof(ctx, "Deriving serviceSubnet from %s (the IP of agent-injector.%s)", ip, env.ManagerNamespace)
		bits := 12
		if ip.Is6() {
			bits = 64
		}
		pfx := netip.PrefixFrom(ip, bits)
		sc, _ := pfx.MarshalBinary()
		oi.ServiceCidrs = [][]byte{sc}
	}

	podCIDRStrategy := env.PodCidrStrategy
	clog.Infof(ctx, "Using podCIDRStrategy: %s", podCIDRStrategy)

	oi.ManagerPodIp = env.PodIp.AsSlice()
	oi.ManagerPodPort = int32(env.ServerPort)
	oi.InjectorSvcHost = fmt.Sprintf("%s.%s", env.AgentInjectorName, env.ManagerNamespace)

	alsoProxy := env.ClientRoutingAlsoProxySubnets
	neverProxy := env.ClientRoutingNeverProxySubnets
	allowConflicting := env.ClientRoutingAllowConflictingSubnets
	clog.Infof(ctx, "Using AlsoProxy: %v", alsoProxy)
	clog.Infof(ctx, "Using NeverProxy: %v", neverProxy)
	clog.Infof(ctx, "Using AllowConflicting: %v", allowConflicting)

	oi.Routing = &rpc.Routing{
		AlsoProxySubnets:        make([]*rpc.IPNet, len(alsoProxy)),
		NeverProxySubnets:       make([]*rpc.IPNet, len(neverProxy)),
		AllowConflictingSubnets: make([]*rpc.IPNet, len(allowConflicting)),
	}
	for i, sn := range alsoProxy {
		oi.Routing.AlsoProxySubnets[i] = iputil.PrefixToRPC(sn)
	}
	for i, sn := range neverProxy {
		oi.Routing.NeverProxySubnets[i] = iputil.PrefixToRPC(sn)
	}

	for i, sn := range allowConflicting {
		oi.Routing.AllowConflictingSubnets[i] = iputil.PrefixToRPC(sn)
	}

	clusterDomain := getClusterDomain(ctx, oi.InjectorSvcIp, env)
	clog.Infof(ctx, "Using cluster domain %q", clusterDomain)
	oi.Dns = &rpc.DNS{
		IncludeSuffixes: env.ClientDnsIncludeSuffixes,
		ExcludeSuffixes: env.ClientDnsExcludeSuffixes,
		KubeIp:          env.PodIp.AsSlice(),
		ClusterDomain:   clusterDomain,
	}

	clog.Infof(ctx, "ExcludeSuffixes: %+v", oi.Dns.ExcludeSuffixes)
	clog.Infof(ctx, "IncludeSuffixes: %+v", oi.Dns.IncludeSuffixes)

	oi.ciSubs = newClusterInfoSubscribers(oi.clusterInfo())

	switch {
	case strings.EqualFold("auto", podCIDRStrategy):
		if !oi.watchNodeSubnets(ctx, false) {
			oi.watchPodSubnets(ctx)
		}
	case strings.EqualFold("nodePodCIDRs", podCIDRStrategy):
		oi.watchNodeSubnets(ctx, true)
	case strings.EqualFold("coverPodIPs", podCIDRStrategy):
		oi.watchPodSubnets(ctx)
	case strings.EqualFold("environment", podCIDRStrategy):
		oi.setSubnetsFromEnv(ctx)
	default:
		clog.Errorf(ctx, "invalid POD_CIDR_STRATEGY %q", podCIDRStrategy)
	}
	return &oi, nil
}

func getClusterDomain(ctx context.Context, svcIp net.IP, env *managerutil.Env) string {
	rcFile := "/etc/resolv.conf"
	name, err := clusterDomainFromResolvConf(rcFile, env.ManagerNamespace)
	if err == nil {
		clog.Infof(ctx, `Cluster domain derived from /etc/resolv.conf search path %q`, name)
		return name
	}
	clog.Infof(ctx, "Unable to extract cluster domain from %s: %v", rcFile, err)

	if managerutil.AgentInjectorEnabled(ctx) {
		desiredMatch := env.AgentInjectorName + "." + env.ManagerNamespace + ".svc."
		addr := svcIp.String()

		for retry := 0; retry <= 2; retry++ {
			if retry > 0 {
				clog.Debugf(ctx, "retry %d of reverse lookup of agent-injector", retry+1)
			}
			if names, err := net.LookupAddr(addr); err == nil {
				for _, name := range names {
					if strings.HasPrefix(name, desiredMatch) {
						clog.Infof(ctx, `Cluster domain derived from agent-injector reverse lookup %q`, name)
						return name[len(desiredMatch):]
					}
				}
			}
			// If no reverse lookups are found containing the cluster domain, then that's probably because the
			// DNS for the service isn't completely setup yet.
			time.Sleep(300 * time.Millisecond)
		}
		clog.Infof(ctx, `Unable to determine cluster domain from CNAME of %s"`, env.AgentInjectorName)
	}
	return "cluster.local."
}

// This code was shamelessly stolen from tailscale/cmd//k8s-operator/svc.go and rewritten to use
// our ResolverFile and return error instead of just logging info.
// Kudos to the authors at Tailscale!
func clusterDomainFromResolvConf(confFile, namespace string) (string, error) {
	conf, err := dnsproxy.ReadResolveFile("/etc/resolv.conf")
	if err != nil {
		return "", err
	}

	if len(conf.Search) < 3 {
		return "", fmt.Errorf("%s contains only %d search domains, at least three expected", confFile, len(conf.Search))
	}
	first := conf.Search[0]
	if !strings.HasPrefix(first, namespace+".svc.") {
		return "", fmt.Errorf("first search domain in %s is %s; expected %s", confFile, first, namespace+".svc.<cluster-domain>")
	}
	second := conf.Search[1]
	if !strings.HasPrefix(second, "svc.") {
		return "", fmt.Errorf("second search domain in %s is %s; expected 'svc.<cluster-domain>'", confFile, second)
	}

	probablyClusterDomain := strings.TrimPrefix(second, "svc.")
	if !strings.HasSuffix(probablyClusterDomain, ".") {
		probablyClusterDomain += "."
	}

	// Trim the trailing dot for backwards compatibility purposes as the
	// cluster domain was previously hardcoded to 'cluster.local' without a
	// trailing dot.
	third := conf.Search[2]
	if !strings.HasSuffix(third, ".") {
		third += "."
	}
	if !strings.EqualFold(third, probablyClusterDomain) {
		return "", fmt.Errorf("expected %s to contain serch domains <namespace>.svc.<cluster-domain>, svc.<cluster-domain>, <cluster-domain>; got %s", confFile, conf.Search)
	}
	return probablyClusterDomain, nil
}

func (oi *info) watchNodeSubnets(ctx context.Context, mustSucceed bool) bool {
	ok, err := k8sapi.CanI(ctx,
		&auth.ResourceAttributes{
			Verb:     "list",
			Resource: "nodes",
		}, &auth.ResourceAttributes{
			Verb:     "watch",
			Resource: "nodes",
		})
	if err != nil || !ok {
		return false
	}

	informerFactory := informer.GetK8sFactory(ctx, "")
	nodeController := informerFactory.Core().V1().Nodes()
	nodeLister := nodeController.Lister()
	nodeInformer := nodeController.Informer()

	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())

	retriever, err := newNodeWatcher(ctx, nodeLister, nodeInformer)
	if err != nil {
		if mustSucceed {
			clog.Errorf(ctx, "failed to create node watcher: %v", err)
		}
		return false
	}
	if !retriever.viable(ctx) {
		if mustSucceed {
			clog.Errorf(ctx, "Unable to derive subnets from nodes")
		}
		return false
	}
	clog.Infof(ctx, "Deriving subnets from podCIRs of nodes")
	go oi.watchSubnets(ctx, retriever)
	return true
}

func getInjectorSvcIP(ctx context.Context, env *managerutil.Env, client typedCore.CoreV1Interface) ([]byte, int32, error) {
	sc, err := client.Services(env.ManagerNamespace).Get(ctx, env.AgentInjectorName, meta.GetOptions{})
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
	ip, err := netip.ParseAddr(sc.Spec.ClusterIP)
	if err != nil {
		return nil, 0, err
	}
	return ip.AsSlice(), p, nil
}

func (oi *info) watchPodSubnets(ctx context.Context) {
	podIP, _ := netip.AddrFromSlice(oi.ManagerPodIp)
	retriever := newPodWatcher(ctx, podIP)
	if !retriever.viable(ctx) {
		clog.Errorf(ctx, "Unable to derive subnets from IPs of pods")
		return
	}
	clog.Infof(ctx, "Deriving subnets from IPs of pods")
	go oi.watchSubnets(ctx, retriever)
}

func (oi *info) setSubnetsFromEnv(ctx context.Context) bool {
	subnets := managerutil.GetEnv(ctx).PodCidrs
	if len(subnets) > 0 {
		mgrIp, _ := netip.AddrFromSlice(oi.ManagerPodIp)
		if !slices.ContainsFunc(subnets, func(s netip.Prefix) bool { return s.Contains(mgrIp) }) {
			sn := netip.PrefixFrom(mgrIp, mgrIp.BitLen())
			clog.Infof(ctx, "Adding %s for the traffic-manager pod, because it was not included in POD_CIDRS environment", sn)
			subnets = append(subnets, sn)
		}
		oi.PodSubnets = iputil.PrefixesToRPC(subnets)
		oi.ciSubs.notify(ctx, oi.clusterInfo())
		clog.Infof(ctx, "Using subnets from POD_CIDRS environment variable")
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

func (oi *info) ServiceIP() net.IP {
	return oi.InjectorSvcIp
}

func (oi *info) ClusterDomain() string {
	return oi.Dns.ClusterDomain
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
		ManagerPodIp:    oi.ManagerPodIp,
		ManagerPodPort:  oi.ManagerPodPort,
		InjectorSvcIp:   oi.InjectorSvcIp,
		InjectorSvcPort: oi.InjectorSvcPort,
		InjectorSvcHost: oi.InjectorSvcHost,
		Routing:         rt,
		Dns:             oi.Dns,
	}
	if len(oi.ServiceCidrs) > 0 {
		var pfx netip.Prefix
		_ = pfx.UnmarshalBinary(oi.ServiceCidrs[0])
		ci.ServiceSubnet = iputil.PrefixToRPC(pfx)
		ci.ServiceCidrs = slices.Clone(oi.ServiceCidrs)
	}
	ci.PodSubnets = slices.Clone(oi.PodSubnets)
	return ci
}

func (oi *info) watchSubnets(ctx context.Context, retriever subnetRetriever) {
	retriever.changeNotifier(ctx, func(subnets subnet.Set) {
		oi.PodSubnets = iputil.PrefixesToRPC(subnets.AppendSortedTo(nil))
		oi.ciSubs.notify(ctx, oi.clusterInfo())
	})
}
