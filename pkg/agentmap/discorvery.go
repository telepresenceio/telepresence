package agentmap

import (
	"context"
	"errors"
	"fmt"

	core "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
)

func FindOwnerWorkload(ctx context.Context, obj k8sapi.Object) (k8sapi.Workload, error) {
	refs := obj.GetOwnerReferences()
	for i := range refs {
		if or := &refs[i]; or.Controller != nil && *or.Controller {
			wl, err := tracing.GetWorkload(ctx, or.Name, obj.GetNamespace(), or.Kind)
			if err != nil {
				var uwkErr k8sapi.UnsupportedWorkloadKindError
				if errors.As(err, &uwkErr) {
					// There can only be one managing controller. If it's of an unsupported
					// type, then the object that it controls is considered the owner, unless
					// it's a pod, in which case it has no owner.
					break
				}
				return nil, err
			}
			return FindOwnerWorkload(ctx, wl)
		}
	}
	if wl, ok := obj.(k8sapi.Workload); ok {
		return wl, nil
	}
	return nil, fmt.Errorf("unable to find workload owner for %s.%s", obj.GetName(), obj.GetNamespace())
}

func findServicesForPod(ctx context.Context, pod *core.PodTemplateSpec, svcName string) ([]k8sapi.Object, error) {
	switch {
	case svcName != "":
		svc, err := k8sapi.GetService(ctx, svcName, pod.Namespace)
		if err != nil {
			if k8sErrors.IsNotFound(err) {
				return nil, fmt.Errorf(
					"unable to find service %s specified by annotation %s declared in pod %s.%s",
					svcName, ServiceNameAnnotation, pod.Name, pod.Namespace)
			}
			return nil, err
		}
		return []k8sapi.Object{svc}, nil
	case len(pod.Labels) > 0:
		lbs := labels.Set(pod.Labels)
		svcs, err := findServicesSelecting(ctx, pod.Namespace, lbs)
		if err != nil {
			return nil, err
		}
		if len(svcs) > 0 {
			return svcs, nil
		}
		return nil, fmt.Errorf("unable to find services that selects pod %s.%s using labels %s", pod.Name, pod.Namespace, lbs)
	default:
		return nil, fmt.Errorf("unable to resolve a service using pod %s.%s because it has no labels", pod.Name, pod.Namespace)
	}
}

func findServicesSelecting(c context.Context, namespace string, lbs labels.Labels) ([]k8sapi.Object, error) {
	ss, err := k8sapi.Services(c, namespace, nil)
	if err != nil {
		return nil, err
	}
	var ms []k8sapi.Object
	for _, s := range ss {
		if sl, err := s.Selector(); err != nil {
			return nil, err
		} else if sl != nil && !sl.Empty() && sl.Matches(lbs) {
			ms = append(ms, s)
		}
	}
	return ms, nil
}

// findContainerMatchingPort finds the container that matches the given ServicePort. The match is
// made using Protocol, and the Name or the ContainerPort field of each port in each container
// depending on if  the service port is symbolic or numeric. The first container with a matching
// port is returned along with the index of the container port that matched.
//
// The first container with no ports at all is returned together with a port index of -1, in case
// no port match could be made and the service port is numeric. This enables intercepts of containers
// that indeed do listen a port but lack a matching port description in the manifest, which is what
// you get if you do:
//
//	kubectl create deploy my-deploy --image my-image
//	kubectl expose deploy my-deploy --port 80 --target-port 8080
func findContainerMatchingPort(port *core.ServicePort, cns []core.Container) (*core.Container, int) {
	// The protocol of the targetPort must match the protocol of the containerPort because it is
	// not illegal to listen with both TCP and UDP on the same port.
	proto := core.ProtocolTCP
	if port.Protocol != "" {
		proto = port.Protocol
	}
	protoEqual := func(p core.Protocol) bool {
		return p == proto || p == "" && proto == core.ProtocolTCP
	}

	if port.TargetPort.Type == intstr.String {
		portName := port.TargetPort.StrVal
		for ci := range cns {
			cn := &cns[ci]
			for pi := range cn.Ports {
				p := &cn.Ports[pi]
				if p.Name == portName && protoEqual(p.Protocol) {
					return cn, pi
				}
			}
		}
	} else {
		portNum := port.TargetPort.IntVal
		if portNum == 0 {
			// The targetPort default is the value of the port field.
			portNum = port.Port
		}
		for ci := range cns {
			cn := &cns[ci]
			for pi := range cn.Ports {
				p := &cn.Ports[pi]
				if p.ContainerPort == portNum && protoEqual(p.Protocol) {
					return cn, pi
				}
			}
		}
		// As a last resort, also consider containers that don't expose their ports at all. Those
		// containers match all ports because it's unknown what they might be listening to.
		for ci := range cns {
			cn := &cns[ci]
			if len(cn.Ports) == 0 {
				return cn, -1
			}
		}
	}
	return nil, 0
}
