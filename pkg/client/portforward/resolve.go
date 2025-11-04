package portforward

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"time"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/watch"
	watchtools "k8s.io/client-go/tools/watch"
	"k8s.io/kubectl/pkg/polymorphichelpers"
	"k8s.io/kubectl/pkg/util/podutils"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func ResolveServiceAndPort(ctx context.Context, name, namespace string, portName string, proto types.Proto) (pap types.AddrPortProto, err error) {
	if pn, err := strconv.Atoi(name); err == nil {
		if ip, err := netip.ParseAddr(name); err == nil {
			return types.AddrPortProto{
				AddrPort: netip.AddrPortFrom(ip, uint16(pn)),
				Proto:    proto,
			}, nil
		}
	}
	svcObj, err := k8sapi.GetService(ctx, name, namespace)
	if err != nil {
		return pap, err
	}
	svc, _ := k8sapi.ServiceImpl(svcObj)
	if svc.Spec.ClusterIP == core.ClusterIPNone {
		return pap, fmt.Errorf("service '%s' is not accessible from outside the cluster", name)
	}
	ip, err := netip.ParseAddr(svc.Spec.ClusterIP)
	if err != nil {
		return pap, fmt.Errorf("unable to parse ClusterIP %q of service '%s': %v", svc.Spec.ClusterIP, name, err)
	}
	svcPort, err := servicePortByName(svc, portName, core.Protocol(proto.String()))
	if err != nil {
		return pap, err
	}
	return types.AddrPortProto{
		AddrPort: netip.AddrPortFrom(ip, uint16(svcPort.Port)),
		Proto:    types.FromK8sProtocol(svcPort.Protocol),
	}, nil
}

func ResolveSvcToPod(ctx context.Context, name, namespace, portName string) (pa *PodAddress, err error) {
	// Get the service.
	pa = new(PodAddress)
	pa.FromSvc = true
	pa.Namespace = namespace
	svcObj, err := k8sapi.GetService(ctx, name, namespace)
	if err != nil {
		return pa, err
	}
	svc, _ := k8sapi.ServiceImpl(svcObj)
	svcPort, err := servicePortByName(svc, portName, "")
	if err != nil {
		return pa, err
	}

	// Resolve the Service to a Pod.
	var selector labels.Selector
	var podNS string
	podNS, selector, err = polymorphichelpers.SelectorsForObject(svc)
	if err != nil {
		return pa, fmt.Errorf("cannot attach to %T: %v", svc, err)
	}
	timeout := func() time.Duration {
		if deadline, ok := ctx.Deadline(); ok {
			return time.Until(deadline)
		}
		// Fall back to the same default as --pod-running-timeout.
		return time.Minute
	}()

	sortBy := func(pods []*core.Pod) sort.Interface { return sort.Reverse(podutils.ActivePods(pods)) }
	var pod *core.Pod
	pod, err = getFirstPod(ctx, podNS, selector.String(), timeout, sortBy)
	if err != nil {
		return pa, fmt.Errorf("cannot find first pod for %s.%s: %v", name, namespace, err)
	}
	pa.Name = pod.Name
	pa.Port, err = containerPortNumber(pod, svcPort.TargetPort)
	pa.Proto = types.FromK8sProtocol(svcPort.Protocol)
	pa.PodID = pod.UID
	if err != nil {
		return pa, fmt.Errorf("cannot find first container port %s.%s: %v", pod.Name, pod.Namespace, err)
	}
	return pa, nil
}

func servicePortByName(svc *core.Service, name string, proto core.Protocol) (*core.ServicePort, error) {
	sps := svc.Spec.Ports
	if proto == "" {
		proto = core.ProtocolTCP
	}
	if pn, err := strconv.Atoi(name); err == nil {
		for si := range sps {
			sp := &sps[si]
			if sp.Port == int32(pn) && (proto == sp.Protocol || proto == core.ProtocolTCP && sp.Protocol == "") {
				return sp, nil
			}
		}
		return nil, fmt.Errorf("service '%s' does not have %s port number '%d'", svc.Name, proto, pn)
	}
	for si := range sps {
		sp := &sps[si]
		if sp.Name == name && (proto == sp.Protocol || proto == core.ProtocolTCP && sp.Protocol == "") {
			return sp, nil
		}
	}
	return nil, fmt.Errorf("service '%s' does not have a %s port named '%s'", svc.Name, proto, name)
}

func containerPortNumber(pod *core.Pod, port intstr.IntOrString) (uint16, error) {
	if port.Type == intstr.Int {
		// It's not required for the container to declare the port.
		return uint16(port.IntVal), nil
	}
	name := port.StrVal
	cns := pod.Spec.Containers
	for ci := range cns {
		cn := &cns[ci]
		for pi := range cn.Ports {
			cp := &cn.Ports[pi]
			if cp.Name == name {
				return uint16(cp.ContainerPort), nil
			}
		}
	}
	return 0, fmt.Errorf("pod '%s' does not have a port named '%s'", pod.Name, name)
}

func resolve(ctx context.Context, addr string) (pa *PodAddress, err error) {
	kind, name, namespace, port, podID, err := parseAddr(addr)
	if err != nil {
		dlog.Errorf(ctx, "cannot resolve addr %s: %v", addr, err)
		return nil, err
	}

	if kind == "svc" {
		// Get the service.
		return ResolveSvcToPod(ctx, name, namespace, port)
	}

	var pn uint16
	if p, err := strconv.ParseUint(port, 10, 16); err == nil {
		pn = uint16(p)
	}
	if pn != 0 && podID != "" {
		return &PodAddress{Name: name, Namespace: namespace, Port: pn, PodID: podID}, nil
	}

	// Get the pod.
	podObj, err := k8sapi.GetPod(ctx, name, namespace)
	if err != nil {
		return pa, fmt.Errorf("unable to get %s %s.%s: %w", kind, name, namespace, err)
	}
	pod, _ := k8sapi.PodImpl(podObj)
	if pn == 0 {
		pn, err = containerPortNumber(pod, intstr.Parse(port))
		if err != nil {
			return pa, err
		}
	}
	return &PodAddress{
		Name:      pod.Name,
		Namespace: pod.Namespace,
		Port:      pn,
		PodID:     pod.UID,
	}, nil
}

// getPods returns a PodList matching the namespace and label selector.
func getPods(ctx context.Context, namespace string, selector string, timeout time.Duration, sortBy func([]*core.Pod) sort.Interface) ([]*core.Pod, error) {
	options := meta.ListOptions{LabelSelector: selector}

	client := k8sapi.GetK8sInterface(ctx).CoreV1()
	podList, err := client.Pods(namespace).List(ctx, options)
	if err != nil {
		return nil, err
	}

	ps := podList.Items
	if len(ps) > 0 {
		pods := make([]*core.Pod, len(ps))
		for i := range ps {
			pods[i] = &ps[i]
		}
		sort.Sort(sortBy(pods))
		return pods, nil
	}

	// Watch until we observe a pod
	options.ResourceVersion = podList.ResourceVersion
	w, err := client.Pods(namespace).Watch(ctx, options)
	if err != nil {
		return nil, err
	}
	defer w.Stop()

	condition := func(event watch.Event) (bool, error) {
		return event.Type == watch.Added || event.Type == watch.Modified, nil
	}
	if timeout != 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	event, err := watchtools.UntilWithoutRetry(ctx, w, condition)
	if err != nil {
		return nil, err
	}

	po, ok := event.Object.(*core.Pod)
	if !ok {
		return nil, fmt.Errorf("%#v is not a pod event", event)
	}
	return []*core.Pod{po}, nil
}

// getFirstPod returns a pod matching the namespace and label selector.
func getFirstPod(ctx context.Context, namespace string, selector string, timeout time.Duration, sortBy func([]*core.Pod) sort.Interface) (*core.Pod, error) {
	podList, err := getPods(ctx, namespace, selector, timeout, sortBy)
	if err != nil {
		return nil, err
	}
	return podList[0], nil
}
