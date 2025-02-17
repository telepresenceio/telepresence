package portforward

import (
	"context"
	"fmt"
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
)

func resolveSvcToPod(ctx context.Context, name, namespace, portName string) (pa *podAddress, err error) {
	// Get the service.
	pa = new(podAddress)
	pa.fromSvc = true
	pa.namespace = namespace
	svcObj, err := k8sapi.GetService(ctx, name, namespace)
	if err != nil {
		return pa, err
	}
	svc, _ := k8sapi.ServiceImpl(svcObj)
	svcPortNumber, err := servicePortByName(svc, portName)
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
	pa.name = pod.Name
	pa.port, err = containerPortByServicePort(svc, pod, svcPortNumber)
	pa.podID = pod.UID
	if err != nil {
		return pa, fmt.Errorf("cannot find first container port %s.%s: %v", pod.Name, pod.Namespace, err)
	}
	return pa, nil
}

func containerPortByServicePort(svc *core.Service, pod *core.Pod, port uint16) (uint16, error) {
	sps := svc.Spec.Ports
	for si := range sps {
		sp := &sps[si]
		if uint16(sp.Port) == port {
			if svc.Spec.ClusterIP == core.ClusterIPNone {
				return port, nil
			}
			tp := sp.TargetPort
			if tp.Type == intstr.Int {
				if tp.IntValue() == 0 {
					// targetPort is omitted, and the IntValue() would be zero
					return uint16(sp.Port), nil
				}
				return uint16(tp.IntValue()), nil
			}
			return containerPortByName(pod, tp.String())
		}
	}
	return port, fmt.Errorf("service %s does not have a service port %d", svc.Name, port)
}

func servicePortByName(svc *core.Service, name string) (uint16, error) {
	if pn, err := strconv.Atoi(name); err == nil {
		return uint16(pn), nil
	}
	sps := svc.Spec.Ports
	for si := range sps {
		sp := &sps[si]
		if sp.Name == name {
			return uint16(sp.Port), nil
		}
	}
	return 0, fmt.Errorf("service '%s' does not have a named port '%s'", svc.Name, name)
}

func containerPortByName(pod *core.Pod, name string) (uint16, error) {
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
	return 0, fmt.Errorf("pod '%s' does not have a named port '%s'", pod.Name, name)
}

func resolve(ctx context.Context, addr string) (pa *podAddress, err error) {
	kind, name, namespace, port, podID, err := parseAddr(addr)
	if err != nil {
		dlog.Errorf(ctx, "cannot resolve addr %s: %v", addr, err)
		return nil, err
	}

	if kind == "svc" {
		// Get the service.
		return resolveSvcToPod(ctx, name, namespace, port)
	}

	var pn uint16
	if p, err := strconv.ParseUint(port, 10, 16); err == nil {
		pn = uint16(p)
	}
	if pn != 0 && podID != "" {
		return &podAddress{name: name, namespace: namespace, port: pn, podID: podID}, nil
	}

	// Get the pod.
	podObj, err := k8sapi.GetPod(ctx, name, namespace)
	if err != nil {
		return pa, fmt.Errorf("unable to get %s %s.%s: %w", kind, name, namespace, err)
	}
	pod, _ := k8sapi.PodImpl(podObj)
	if pn == 0 {
		pn, err = containerPortByName(pod, port)
		if err != nil {
			return pa, err
		}
	}
	return &podAddress{
		name:      pod.Name,
		namespace: pod.Namespace,
		port:      pn,
		podID:     pod.UID,
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
