package setup

import (
	"context"
	"fmt"
	"net"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// servicePortByName finds the port named portName on svc, falling back to
// the sole port when the Service carries exactly one (a customized chart
// install might not preserve the name).
func servicePortByName(svc *corev1.Service, portName string) (corev1.ServicePort, bool) {
	for _, p := range svc.Spec.Ports {
		if p.Name == portName {
			return p, true
		}
	}
	if len(svc.Spec.Ports) == 1 {
		return svc.Spec.Ports[0], true
	}
	return corev1.ServicePort{}, false
}

// loadBalancerIngressAddr returns ing's address: its IP, or its Hostname
// when the IP is unset. Empty when neither is set.
func loadBalancerIngressAddr(ing corev1.LoadBalancerIngress) string {
	if ing.IP != "" {
		return ing.IP
	}
	return ing.Hostname
}

// firstLoadBalancerIngressAddr returns the first assigned ingress address on
// svc's LoadBalancer status, or "" when none is assigned yet.
func firstLoadBalancerIngressAddr(svc *corev1.Service) string {
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		if addr := loadBalancerIngressAddr(ing); addr != "" {
			return addr
		}
	}
	return ""
}

// firstAllocatedNodePort returns the first non-zero NodePort among svc's
// ports.
func firstAllocatedNodePort(svc *corev1.Service) (int32, bool) {
	for _, p := range svc.Spec.Ports {
		if p.NodePort != 0 {
			return p.NodePort, true
		}
	}
	return 0, false
}

// resolveServiceDialAddr resolves the address to dial for svc's portName
// port: a LoadBalancer's ingress address, or a NodePort together with a node
// address (ExternalIP preferred, InternalIP as fallback). label names the
// service in error messages (e.g. "the QUIC service", "the external
// endpoint service").
func resolveServiceDialAddr(ctx context.Context, ki kubernetes.Interface, svc *corev1.Service, portName, label string) (string, error) {
	switch svc.Spec.Type {
	case corev1.ServiceTypeLoadBalancer:
		port, ok := servicePortByName(svc, portName)
		if !ok {
			return "", fmt.Errorf("%s has no identifiable %s port", label, portName)
		}
		if addr := firstLoadBalancerIngressAddr(svc); addr != "" {
			return net.JoinHostPort(addr, strconv.Itoa(int(port.Port))), nil
		}
		return "", fmt.Errorf("%s has no assigned LoadBalancer ingress", label)
	case corev1.ServiceTypeNodePort:
		port, ok := servicePortByName(svc, portName)
		if !ok || port.NodePort == 0 {
			return "", fmt.Errorf("%s has no allocated node port", label)
		}
		addr, err := firstNodeAddress(ctx, ki)
		if err != nil {
			return "", err
		}
		return net.JoinHostPort(addr, strconv.Itoa(int(port.NodePort))), nil
	default:
		return "", fmt.Errorf("service type %s has no externally reachable address", svc.Spec.Type)
	}
}
