package pfs

import (
	"slices"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/watcher"
)

// ServiceTCPPort is a ServicePort with TCP protocol (port-forward only works with TCP).
type ServiceTCPPort struct {
	Name       string
	Port       int32
	TargetPort intstr.IntOrString
}

// Service contains only what's necessary to select the pod for a given TCP port.
type Service struct {
	Ports    []ServiceTCPPort
	Selector map[string]string
}

func (s *Service) Equal(o *Service) bool {
	return slices.Equal(s.Ports, o.Ports) && maps.Equal(s.Selector, o.Selector)
}

// ServiceFromIP returns a state that maintains iputil.IPKey to Service mappings. The state can be used when finding the
// Service for a specific IP.
func ServiceFromIP() watcher.EventHandlerState[*core.Service, iputil.IPKey, *Service] {
	ws := watcher.NewState[*core.Service, iputil.IPKey, *Service](
		func(service *core.Service) []iputil.IPKey {
			var ips []iputil.IPKey
			if ip := iputil.Parse(service.Spec.ClusterIP); ip != nil {
				ips = append(ips, iputil.IPKey(ip))
			}
			for _, c := range service.Spec.ClusterIPs {
				if ip := iputil.Parse(c); ip != nil {
					ips = append(ips, iputil.IPKey(ip))
				}
			}
			for _, c := range service.Spec.ExternalIPs {
				if ip := iputil.Parse(c); ip != nil {
					ips = append(ips, iputil.IPKey(ip))
				}
			}
			return ips
		},
		func(service *core.Service) *Service {
			s := &service.Spec
			sps := s.Ports
			ports := make([]ServiceTCPPort, 0, len(sps))
			for i := range sps {
				sp := &sps[i]
				if sp.Protocol == "" || sp.Protocol == core.ProtocolTCP {
					ports = append(ports, ServiceTCPPort{
						Name:       sp.Name,
						Port:       sp.Port,
						TargetPort: sp.TargetPort,
					})
				}
			}
			return &Service{
				Ports:    ports,
				Selector: maps.Copy(s.Selector),
			}
		})
	return ws
}
