package cluster

import (
	"context"
	"net"
	"regexp"
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

type Info interface {
	// Watch changes of an ClusterInfo and write them on the given stream
	Watch(context.Context, rpc.Manager_WatchClusterInfoServer) error

	// GetClusterID returns the ClusterID
	GetClusterID() string
}

type info struct {
	rpc.ClusterInfo
	accLock sync.Mutex
	Pods    []kates.Pod
	Nodes   []kates.Node
	waiter  sync.Cond

	// podCIDRMap keeps track of the current set of pod CIDRs
	podCIDRMap map[iputil.IPKey]int
	// clusterID is the UID of the default namespace
	clusterID string
}

func NewInfo(ctx context.Context) Info {
	oi := info{}
	oi.waiter.L = &oi.accLock

	client := managerutil.GetKatesClient(ctx)
	if client == nil {
		// running in test environment
		return &oi
	}

	// Get the clusterID from the default namespaces
	// We use a default clusterID because we don't want to fail if
	// the traffic-manager doesn't have the ability to get the namespace
	ns := &kates.Namespace{
		TypeMeta:   kates.TypeMeta{Kind: "Namespace"},
		ObjectMeta: kates.ObjectMeta{Name: "default"},
	}

	if err := client.Get(ctx, ns, ns); err != nil {
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
		svc := kates.Service{TypeMeta: metav1.TypeMeta{Kind: "Service"}, ObjectMeta: dnsService}
		if err := client.Get(ctx, &svc, &svc); err == nil {
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
	svc := kates.Service{
		TypeMeta: metav1.TypeMeta{
			Kind: "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: env.ManagerNamespace,
			Name:      "t2-tst-dummy",
		},
		Spec: corev1.ServiceSpec{
			Ports:     []kates.ServicePort{{Port: 443}},
			ClusterIP: "1.1.1.1",
		},
	}

	if err := client.Create(ctx, &svc, &svc); err != nil {
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

	podCIDRs := oi.getPodCIDRsFromNodes(ctx)
	if len(podCIDRs) == 0 {
		// Some clusters (e.g. Amazon EKS) doesn't assign podCIDRs to nodes
		// by default so we compute those CIDRs by looking at all pods instead.
		dlog.Infof(ctx, "Deriving subnets from IPs of pods")
		podCIDRs = oi.getPodCIDRsFromPods(ctx)
		oi.watchSubnets(ctx, "Pods", "pod", oi.podCIDRsFromPods)
	} else {
		dlog.Infof(ctx, "Deriving subnets from podCIRs of nodes")
		oi.watchSubnets(ctx, "Nodes", "node", oi.podCIDRsFromNodes)
	}
	oi.updateSubnets(podCIDRs)
	return &oi
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
		return err
	}

	for ctx.Err() == nil {
		oi.accLock.Lock()
		oi.waiter.Wait()
		ci = oi.clusterInfo()
		oi.accLock.Unlock()
		dlog.Debugf(ctx, "WatchClusterInfo sending update")
		if err := oiStream.Send(ci); err != nil {
			return err
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

func (oi *info) watchSubnets(ctx context.Context, name, kind string, retriever func(context.Context) []*net.IPNet) {
	acc := managerutil.GetKatesClient(ctx).Watch(ctx,
		kates.Query{
			Name: name,
			Kind: kind,
		})

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-acc.Changed():
				if oi.accUpdate(ctx, acc) && oi.updateSubnets(retriever(ctx)) {
					oi.waiter.Broadcast()
				}
			}
		}
	}()
}

func (oi *info) updateSubnets(podCIDRs []*net.IPNet) bool {
	changed := true
	if len(podCIDRs) == len(oi.podCIDRMap) {
		changed = false // assume equal

		// Check if all IPs are found and that their masks are equal
		for _, cidr := range podCIDRs {
			if ones, ok := oi.podCIDRMap[iputil.IPKey(cidr.IP)]; ok {
				newOnes, _ := cidr.Mask.Size()
				if newOnes == ones {
					continue
				}
			}
			changed = true
			break
		}
	}
	if changed {
		oi.podCIDRMap = makeCIDRMap(podCIDRs)
		oi.PodSubnets = toRPCSubnets(podCIDRs)
	}
	return changed
}

// accUpdate updates the relevant field in the info and recovers any panics that
// may be raised by the accumulator Update() call and logs them.
func (oi *info) accUpdate(ctx context.Context, a *kates.Accumulator) bool {
	oi.accLock.Lock()
	defer func() {
		if r := recover(); r != nil {
			dlog.Errorf(ctx, "Accumulator updated failed: %v", derror.PanicToError(r))
		}
		oi.accLock.Unlock()
	}()
	return a.Update(oi)
}

func (oi *info) getPodCIDRsFromNodes(ctx context.Context) []*net.IPNet {
	if err := managerutil.GetKatesClient(ctx).List(ctx, kates.Query{Kind: "Node"}, &oi.Nodes); err != nil {
		dlog.Errorf(ctx, "failed to get nodes: %v", err)
		return nil
	}
	return oi.podCIDRsFromNodes(ctx)
}

func (oi *info) podCIDRsFromNodes(ctx context.Context) []*net.IPNet {
	subnets := make([]*net.IPNet, 0, len(oi.Nodes))
	for i := range oi.Nodes {
		node := &oi.Nodes[i]
		spec := node.Spec
		cidrs := spec.PodCIDRs
		if len(cidrs) == 0 && spec.PodCIDR != "" {
			cidrs = []string{spec.PodCIDR}
		}
		for _, cs := range cidrs {
			_, cidr, err := net.ParseCIDR(cs)
			if err != nil {
				dlog.Errorf(ctx, "unable to parse podCIDR %q in node %s", cs, node.Name)
				continue
			}
			subnets = append(subnets, cidr)
		}
	}
	return subnets
}

func (oi *info) getPodCIDRsFromPods(ctx context.Context) []*net.IPNet {
	if err := managerutil.GetKatesClient(ctx).List(ctx, kates.Query{Kind: "Pod"}, &oi.Pods); err != nil {
		dlog.Errorf(ctx, "failed to get pods: %v", err)
		return nil
	}
	return oi.podCIDRsFromPods(ctx)
}

func (oi *info) podCIDRsFromPods(ctx context.Context) []*net.IPNet {
	ips := make(iputil.IPs, 0, len(oi.Pods))
	for i := range oi.Pods {
		pod := &oi.Pods[i]
		status := pod.Status
		podIPs := status.PodIPs
		if len(podIPs) == 0 && status.PodIP != "" {
			podIPs = []corev1.PodIP{{IP: status.PodIP}}
		}
		for _, ps := range podIPs {
			ip := iputil.Parse(ps.IP)
			if ip == nil {
				dlog.Errorf(ctx, "unable to parse IP %q in pod %s.%s", ps.IP, pod.Name, pod.Namespace)
				continue
			}
			ips = append(ips, ip)
		}
	}
	return subnet.CoveringCIDRs(ips)
}

func makeCIDRMap(cidrs []*net.IPNet) map[iputil.IPKey]int {
	m := make(map[iputil.IPKey]int, len(cidrs))
	for _, cidr := range cidrs {
		m[iputil.IPKey(cidr.IP)], _ = cidr.Mask.Size()
	}
	return m
}

func toRPCSubnets(cidrs []*net.IPNet) []*rpc.IPNet {
	subnets := make([]*rpc.IPNet, len(cidrs))
	for i, cidr := range cidrs {
		subnets[i] = iputil.IPNetToRPC(cidr)
	}
	return subnets
}
