package pfs

import (
	"maps"
	"net"
	"slices"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
	"github.com/telepresenceio/telepresence/v2/pkg/watcher"
)

// ContainerTCPPort is a ContainerPort with TCP protocol (port-forward only works with TCP).
type ContainerTCPPort struct {
	Name          string
	ContainerPort int32
}

// Pod contains only the fields relevant to select it from a service and to produce a name, namespace, and port.
type Pod struct {
	Name      string
	Namespace string
	Ports     []ContainerTCPPort
	Labels    labels.Set
}

func (p *Pod) Equal(o *Pod) bool {
	return p.Name == o.Name && p.Namespace == o.Namespace && slices.Equal(p.Ports, o.Ports) && maps.Equal(p.Labels, o.Labels)
}

// PodFromIP returns a state that maintains iputil.IPKey to Pod mappings. The state can be used when finding the
// pod for a specific IP or when finding a pod that matches a specific service selector.
func PodFromIP() watcher.EventHandlerState[*core.Pod, iputil.IPKey, *Pod] {
	return watcher.NewState[*core.Pod, iputil.IPKey, *Pod](
		func(pod *core.Pod) []iputil.IPKey {
			var ips []iputil.IPKey
			if ip := iputil.Parse(pod.Status.PodIP); ip != nil {
				ips = append(ips, iputil.IPKey(ip))
			}
			for _, pi := range pod.Status.PodIPs {
				if ip := iputil.Parse(pi.IP); ip != nil {
					ips = append(ips, iputil.IPKey(ip))
				}
			}
			return ips
		},
		func(pod *core.Pod) *Pod {
			var ports []ContainerTCPPort
			cns := pod.Spec.Containers
			for i := range cns {
				pss := cns[i].Ports
				for pi := range pss {
					ps := &pss[pi]
					if ps.Protocol == "" || ps.Protocol == core.ProtocolTCP {
						ports = append(ports, ContainerTCPPort{
							Name:          ps.Name,
							ContainerPort: ps.ContainerPort,
						})
					}
				}
			}
			return &Pod{
				Name:      pod.Name,
				Namespace: pod.Namespace,
				Labels:    pod.Labels,
				Ports:     ports,
			}
		})
}

// FindPodForService uses the given service's selector to find a matching pod. The found pod is returned along with
// the container port that matches the given service port. The function returns nil, 0 if no matching pod is found.
func FindPodForService(svc *Service, port int32, pods watcher.State[iputil.IPKey, *Pod]) (found *Pod, containerPort int32) {
	var svcPort *ServiceTCPPort
	for i := range svc.Ports {
		sp := &svc.Ports[i]
		if sp.Port == port {
			svcPort = sp
			break
		}
	}
	if svcPort == nil {
		return nil, 0
	}

	var containerPortName string
	if svcPort.TargetPort.Type == intstr.String {
		// A named target port must find a matching container port, or the
		// pod won't be reachable.
		containerPortName = svcPort.TargetPort.StrVal
	} else {
		// The containerPort is determined by the service. The pod might
		// listen to this port even if no container declares it, so it's
		// not used in the filter below.
		containerPort = svcPort.TargetPort.IntVal
		if containerPort == 0 {
			containerPort = svcPort.Port
		}
	}

	selector := labels.SelectorFromSet(svc.Selector)
	found = pods.FindFirst(func(key iputil.IPKey, pod *Pod) bool {
		if selector.Matches(pod.Labels) {
			if containerPort != 0 {
				return true
			}
			pp := pod.Ports
			for i := range pp {
				p := &pp[i]
				if p.Name == containerPortName {
					containerPort = p.ContainerPort
					return true
				}
			}
		}
		return false
	})
	if found == nil {
		containerPort = 0
	}
	return found, containerPort
}

// PodSubnets returns the subnets necessary to cover all pod IPs in the given pods state.
func PodSubnets(pods watcher.State[iputil.IPKey, *Pod]) []*net.IPNet {
	keys := pods.Keys()
	ips := make([]net.IP, len(keys))
	for i, ik := range keys {
		ips[i] = ik.IP()
	}
	return subnet.CoveringCIDRs(ips)
}
