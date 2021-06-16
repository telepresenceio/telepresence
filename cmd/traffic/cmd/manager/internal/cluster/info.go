package cluster

import (
	"context"
	"net"
	"regexp"
	"sync"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

type Info interface {
	// Watch changes of an ClusterInfo and write them on the given stream
	Watch(context.Context, rpc.Manager_WatchClusterInfoServer) error
}

type info struct {
	rpc.ClusterInfo
	accLock sync.Mutex
	Pods    []kates.Pod
	Nodes   []kates.Node
	waiter  sync.Cond
}

func NewInfo(ctx context.Context) Info {
	oi := info{}
	oi.waiter.L = &oi.accLock
	oi.PodSubnets = make([]*rpc.IPNet, 0, 2)

	client := managerutil.GetKatesClient(ctx)
	if client == nil {
		// running in test environment
		return &oi
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

	// make an attempt to create a service with ClusterIP that is out of range and then
	// check the error message for the correct range as suggested tin the second answer here:
	//   https://stackoverflow.com/questions/44190607/how-do-you-find-the-cluster-service-cidr-of-a-kubernetes-cluster
	// This requires an additional permission to create a service, which the traffic-manager
	// should have.
	svc := kates.Service{
		TypeMeta: metav1.TypeMeta{
			Kind: "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "t2-tst-dummy",
		},
		Spec: v1.ServiceSpec{
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
		}
	}

	if oi.ServiceSubnet == nil && oi.KubeDnsIp != nil {
		// Using a "kubectl cluster-info dump" or scanning all services generates a lot of unwanted traffic
		// and would quite possibly also require elevated permissions, so instead, we derive the service subnet
		// from the kubeDNS IP. This is cheating but a cluster may only have one service subnet and the mask is
		// unlikely to cover less than half the bits.
		dlog.Infof(ctx, "Deriving serviceSubnet from %s (the IP of kube-dns.kube-system)", oi.KubeDnsIp)
		bits := len(oi.KubeDnsIp) * 8
		ones := bits / 2
		mask := net.CIDRMask(ones, bits) // will yield a 16 bit mask on IPv4 and 64 bit mask on IPv6.
		oi.ServiceSubnet = &rpc.IPNet{Ip: net.IP(oi.KubeDnsIp).Mask(mask), Mask: int32(ones)}
	}

	oi.PodSubnets = oi.getPodCIDRsFromNodes(ctx)
	if len(oi.PodSubnets) == 0 {
		// Some clusters (e.g. Amazon EKS) doesn't assign podCIDRs to nodes
		// by default so we compute those CIDRs by looking at all pods instead.
		dlog.Infof(ctx, "Deriving subnets from IPs of pods")
		oi.PodSubnets = oi.getPodCIDRsFromPods(ctx)
		oi.watchSubnets(ctx, "Pods", "pod", oi.podCIDRsFromPods)
	} else {
		dlog.Infof(ctx, "Deriving subnets from podCIRs of nodes")
		oi.watchSubnets(ctx, "Nodes", "node", oi.podCIDRsFromNodes)
	}
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

// clusterInfo must be called with accLock locked
func (oi *info) clusterInfo() *rpc.ClusterInfo {
	ci := &rpc.ClusterInfo{
		KubeDnsIp:     oi.KubeDnsIp,
		ServiceSubnet: oi.ServiceSubnet,
		PodSubnets:    make([]*rpc.IPNet, len(oi.PodSubnets)),
	}
	copy(ci.PodSubnets, oi.PodSubnets)
	return ci
}

func (oi *info) watchSubnets(ctx context.Context, name, kind string, retriever func(context.Context) []*manager.IPNet) {
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
				if oi.accUpdate(ctx, acc) {
					oi.PodSubnets = retriever(ctx)
					oi.waiter.Broadcast()
				}
			}
		}
	}()
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

func (oi *info) getPodCIDRsFromNodes(ctx context.Context) []*rpc.IPNet {
	if err := managerutil.GetKatesClient(ctx).List(ctx, kates.Query{Kind: "Node"}, &oi.Nodes); err != nil {
		dlog.Errorf(ctx, "failed to get nodes: %v", err)
		return nil
	}
	return oi.podCIDRsFromNodes(ctx)
}

func (oi *info) podCIDRsFromNodes(ctx context.Context) []*rpc.IPNet {
	subnets := make([]*rpc.IPNet, 0, len(oi.Nodes))
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
			dlog.Infof(ctx, "Using podCIDR %s from node %s", cidr, node.Name)
			subnets = append(subnets, iputil.IPNetToRPC(cidr))
		}
	}
	return subnets
}

func (oi *info) getPodCIDRsFromPods(ctx context.Context) []*rpc.IPNet {
	if err := managerutil.GetKatesClient(ctx).List(ctx, kates.Query{Kind: "Pod"}, &oi.Pods); err != nil {
		dlog.Errorf(ctx, "failed to get pods: %v", err)
		return nil
	}
	return oi.podCIDRsFromPods(ctx)
}

func (oi *info) podCIDRsFromPods(ctx context.Context) []*rpc.IPNet {
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
	cidrs := subnet.CoveringCIDRs(ips)
	subnets := make([]*rpc.IPNet, len(cidrs))
	for i, cidr := range cidrs {
		subnets[i] = iputil.IPNetToRPC(cidr)
	}
	return subnets
}
