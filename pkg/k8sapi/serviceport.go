package k8sapi

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"

	core "k8s.io/api/core/v1"
)

// ResolveServicePort fetches the named service and resolves port (a name or a
// numeric port string) against it, returning the service's ClusterIP and the
// matching ServicePort.
func ResolveServicePort(ctx context.Context, name, namespace, port string, proto core.Protocol) (netip.Addr, *core.ServicePort, error) {
	svcObj, err := GetService(ctx, name, namespace)
	if err != nil {
		return netip.Addr{}, nil, err
	}
	svc, _ := ServiceImpl(svcObj)
	if svc.Spec.ClusterIP == core.ClusterIPNone {
		return netip.Addr{}, nil, fmt.Errorf("service '%s' is not accessible from outside the cluster", name)
	}
	ip, err := netip.ParseAddr(svc.Spec.ClusterIP)
	if err != nil {
		return netip.Addr{}, nil, fmt.Errorf("unable to parse ClusterIP %q of service '%s': %v", svc.Spec.ClusterIP, name, err)
	}
	sp, err := ServicePortByName(svc, port, proto)
	if err != nil {
		return netip.Addr{}, nil, err
	}
	return ip, sp, nil
}

// ServicePortByName returns the ServicePort matching name (a port name or a numeric port
// string) and proto. An empty proto defaults to TCP.
func ServicePortByName(svc *core.Service, name string, proto core.Protocol) (*core.ServicePort, error) {
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
