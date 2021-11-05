package cluster

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"

	"github.com/blang/semver"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

const supportedKubeAPIVersion = "1.17.0"

type Info interface {
	// Watch changes of an ClusterInfo and write them on the given stream
	Watch(context.Context, rpc.Manager_WatchClusterInfoServer) error

	// GetClusterID returns the ClusterID
	GetClusterID() string

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
	accLock sync.Mutex
	waiter  sync.Cond

	// clusterID is the UID of the default namespace
	clusterID string
}

func NewInfo(ctx context.Context) Info {
	oi := info{}
	oi.waiter.L = &oi.accLock
	clientset := managerutil.GetK8sClientset(ctx)

	// Validate that the kubernetes server version is supported
	dc := clientset.Discovery()
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

	client := clientset.CoreV1()
	// Get the clusterID from the default namespaces
	// We use a default clusterID because we don't want to fail if
	// the traffic-manager doesn't have the ability to get the namespace
	ns, err := client.Namespaces().Get(ctx, "default", metav1.GetOptions{})
	if err != nil {
		oi.clusterID = "00000000-0000-0000-0000-000000000000"
		dlog.Errorf(ctx, "unable to get `default` namespace: %s, using default clusterID: %s",
			err, oi.clusterID)
	} else {
		oi.clusterID = string(ns.GetUID())
	}

	// places to look for the cluster's DNS service
	dnsServices := []metav1.ObjectMeta{
		{
			Name:      "kube-dns",
			Namespace: "kube-system",
		},
		{
			Name:      "coredns",
			Namespace: "kube-system",
		},
		{
			Name:      "dns-default",
			Namespace: "openshift-dns",
		},
	}
	for _, dnsService := range dnsServices {
		if svc, err := client.Services(dnsService.Namespace).Get(ctx, dnsService.Name, metav1.GetOptions{}); err == nil {
			dlog.Infof(ctx, "Using DNS IP from %s.%s", svc.Name, svc.Namespace)
			oi.KubeDnsIp = iputil.Parse(svc.Spec.ClusterIP)
			break
		}
	}

	apiSvc := "kubernetes.default.svc"
	if cn, err := net.LookupCNAME(apiSvc); err != nil {
		dlog.Infof(ctx, `Unable to determine cluster domain from CNAME of %s: %v"`, err, apiSvc)
		oi.ClusterDomain = "cluster.local."
	} else {
		oi.ClusterDomain = cn[len(apiSvc)+1:]
	}
	dlog.Infof(ctx, "Using cluster domain %q", oi.ClusterDomain)

	// make an attempt to create a service with ClusterIP that is out of range and then
	// check the error message for the correct range as suggested tin the second answer here:
	//   https://stackoverflow.com/questions/44190607/how-do-you-find-the-cluster-service-cidr-of-a-kubernetes-cluster
	// This requires an additional permission to create a service, which the traffic-manager
	// should have.
	env := managerutil.GetEnv(ctx)
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

	if oi.ServiceSubnet == nil && oi.KubeDnsIp != nil {
		// Using a "kubectl cluster-info dump" or scanning all services generates a lot of unwanted traffic
		// and would quite possibly also require elevated permissions, so instead, we derive the service subnet
		// from the kubeDNS IP. This is cheating but a cluster may only have one service subnet and the mask is
		// unlikely to cover less than half the bits.
		dlog.Infof(ctx, "Deriving serviceSubnet from %s (the IP of kube-dns.kube-system)", net.IP(oi.KubeDnsIp))
		bits := len(oi.KubeDnsIp) * 8
		ones := bits / 2
		mask := net.CIDRMask(ones, bits) // will yield a 16 bit mask on IPv4 and 64 bit mask on IPv6.
		oi.ServiceSubnet = &rpc.IPNet{Ip: net.IP(oi.KubeDnsIp).Mask(mask), Mask: int32(ones)}
	}

	podCIDRStrategy := env.PodCIDRStrategy
	dlog.Infof(ctx, "Using podCIDRStrategy: %s", podCIDRStrategy)

	switch {
	case strings.EqualFold("auto", podCIDRStrategy):
		go func() {
			if !oi.watchNodeSubnets(ctx) {
				oi.watchPodSubnets(ctx)
			}
		}()
	case strings.EqualFold("nodePodCIDRs", podCIDRStrategy):
		go oi.watchNodeSubnets(ctx)
	case strings.EqualFold("coverPodIPs", podCIDRStrategy):
		go oi.watchPodSubnets(ctx)
	case strings.EqualFold("environment", podCIDRStrategy):
		oi.setSubnetsFromEnv(ctx)
	default:
		dlog.Errorf(ctx, "invalid POD_CIDR_STRATEGY %q", podCIDRStrategy)
	}
	return &oi
}

func (oi *info) watchNodeSubnets(ctx context.Context) bool {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	informerFactory := informers.NewSharedInformerFactory(managerutil.GetK8sClientset(ctx), 0)
	nodeController := informerFactory.Core().V1().Nodes()
	nodeLister := nodeController.Lister()
	nodeInformer := nodeController.Informer()

	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())

	retriever := newNodeWatcher(ctx, nodeLister, nodeInformer)
	if !retriever.viable(ctx) {
		dlog.Errorf(ctx, "Unable to derive subnets from nodes")
		return false
	}
	dlog.Infof(ctx, "Deriving subnets from podCIRs of nodes")
	oi.watchSubnets(ctx, retriever)
	return true
}

func (oi *info) watchPodSubnets(ctx context.Context) bool {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	informerFactory := informers.NewSharedInformerFactory(managerutil.GetK8sClientset(ctx), 0)
	podController := informerFactory.Core().V1().Pods()
	podLister := podController.Lister()
	podInformer := podController.Informer()

	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())

	retriever := newPodWatcher(ctx, podLister, podInformer)
	if !retriever.viable(ctx) {
		dlog.Errorf(ctx, "Unable to derive subnets from IPs of pods")
		return false
	}
	dlog.Infof(ctx, "Deriving subnets from IPs of pods")
	oi.watchSubnets(ctx, retriever)
	return true
}

func (oi *info) setSubnetsFromEnv(ctx context.Context) bool {
	pcEnv := managerutil.GetEnv(ctx).PodCIDRs
	cidrStrs := strings.Split(pcEnv, " ")
	allOK := len(cidrStrs) > 0
	subnets := make(subnet.Set, len(cidrStrs))
	if allOK {
		for _, s := range cidrStrs {
			_, cidr, err := net.ParseCIDR(s)
			if err != nil {
				dlog.Errorf(ctx, "unable to parse CIDR %q from environment variable POD_CIDRS: %v", s, err)
				allOK = false
				break
			}
			subnets.Add(cidr)
		}
	}
	if allOK {
		dlog.Infof(ctx, "Using subnets from POD_CIDRS environment variable")
		oi.PodSubnets = toRPCSubnets(subnets)
	} else {
		dlog.Errorf(ctx, "unable to parse subnets from POD_CIDRS value %q", pcEnv)
	}
	return allOK
}

// Watch will start by sending an initial snapshot of the ClusterInfo on the given stream
// and then enter a loop where it waits for updates and sends new snapshots.
func (oi *info) Watch(ctx context.Context, oiStream rpc.Manager_WatchClusterInfoServer) error {
	// Send initial snapshot
	dlog.Debugf(ctx, "WatchClusterInfo sending update")
	oi.accLock.Lock()
	ci := oi.clusterInfo()
	oi.accLock.Unlock()
	if err := oiStream.Send(ci); err != nil {
		return status.Error(codes.Internal, fmt.Sprintf("WatchClusterInfo failed to send initial update, %v", err))
	}

	for ctx.Err() == nil {
		oi.accLock.Lock()
		oi.waiter.Wait()
		ci = oi.clusterInfo()
		oi.accLock.Unlock()
		dlog.Debugf(ctx, "WatchClusterInfo sending update")
		if err := oiStream.Send(ci); err != nil {
			return status.Error(codes.Internal, fmt.Sprintf("WatchClusterInfo failed to send update, %v", err))
		}
	}
	return nil
}

func (oi *info) GetClusterID() string {
	return oi.clusterID
}

// clusterInfo must be called with accLock locked
func (oi *info) clusterInfo() *rpc.ClusterInfo {
	ci := &rpc.ClusterInfo{
		KubeDnsIp:     oi.KubeDnsIp,
		ServiceSubnet: oi.ServiceSubnet,
		PodSubnets:    make([]*rpc.IPNet, len(oi.PodSubnets)),
		ClusterDomain: oi.ClusterDomain,
	}
	copy(ci.PodSubnets, oi.PodSubnets)
	return ci
}

func (oi *info) watchSubnets(ctx context.Context, retriever subnetRetriever) {
	retriever.changeNotifier(ctx, func(subnets subnet.Set) {
		oi.PodSubnets = toRPCSubnets(subnets)
		oi.waiter.Broadcast()
	})
}

func toRPCSubnets(cidrMap subnet.Set) []*rpc.IPNet {
	subnets := cidrMap.AppendSortedTo(nil)
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
	clientset := managerutil.GetK8sClientset(ctx)
	client := clientset.CoreV1()
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
			if container.Name == install.AgentContainerName {
				agentPods = append(agentPods, &pod)
				break
			}
		}
	}
	return agentPods, nil
}

// GetTrafficManagerPods gets all pods in the manager's namespace that have
// `traffic-manager` in the name
func (oi *info) GetTrafficManagerPods(ctx context.Context) ([]*corev1.Pod, error) {
	clientset := managerutil.GetK8sClientset(ctx)
	client := clientset.CoreV1()
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
		if strings.Contains(pod.Name, install.ManagerAppName) {
			tmPods = append(tmPods, &pod)
		}
	}
	return tmPods, nil
}
