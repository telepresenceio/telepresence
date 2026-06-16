package state

import (
	"context"
	"sort"
	"strconv"

	netv1 "k8s.io/api/networking/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

type workloadExposure struct {
	desiredReplicas int32
	readyReplicas   int32
	services        []*rpc.ServiceAssociation
}

func describeWorkloadExposure(ctx context.Context, wl k8sapi.Workload) workloadExposure {
	exposure := workloadExposure{
		desiredReplicas: k8sapi.DesiredReplicas(wl),
		readyReplicas:   k8sapi.ReadyReplicas(wl),
	}
	if sc := mutator.GetMap(ctx).Get(wl.GetName(), wl.GetNamespace()); sc != nil {
		exposure.services = serviceAssociationsForSidecar(sc)
		attachRouteAssociations(ctx, sc.Namespace, exposure.services)
	}
	return exposure
}

func serviceAssociationsForSidecar(sc *agentconfig.Sidecar) []*rpc.ServiceAssociation {
	type servicePortKey struct {
		name     string
		portName string
		port     int32
	}
	svcMap := map[string]*rpc.ServiceAssociation{}
	seen := map[servicePortKey]struct{}{}
	for _, cn := range sc.Containers {
		for _, ic := range cn.Intercepts {
			if ic.ServiceName == "" {
				continue
			}
			key := servicePortKey{
				name:     ic.ServiceName,
				portName: ic.ServicePortName,
				port:     int32(ic.ServicePort),
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}

			targetPort := ic.ContainerPortName
			if ic.TargetPortNumeric {
				targetPort = strconv.Itoa(int(ic.ContainerPort))
			}
			service := svcMap[ic.ServiceName]
			if service == nil {
				service = &rpc.ServiceAssociation{
					Name: ic.ServiceName,
				}
				svcMap[ic.ServiceName] = service
			}
			service.Ports = append(service.Ports, &rpc.ServicePort{
				Name:       ic.ServicePortName,
				Port:       int32(ic.ServicePort),
				TargetPort: targetPort,
			})
		}
	}
	if len(svcMap) == 0 {
		return nil
	}

	services := make([]*rpc.ServiceAssociation, 0, len(svcMap))
	for _, service := range svcMap {
		sort.Slice(service.Ports, func(i, j int) bool {
			if service.Ports[i].GetPort() != service.Ports[j].GetPort() {
				return service.Ports[i].GetPort() < service.Ports[j].GetPort()
			}
			return service.Ports[i].GetName() < service.Ports[j].GetName()
		})
		services = append(services, service)
	}
	sort.Slice(services, func(i, j int) bool {
		return services[i].GetName() < services[j].GetName()
	})
	return services
}

// attachRouteAssociations finds all routes in the given namespace that target one of
// the given services and attaches them to the service they target.
func attachRouteAssociations(ctx context.Context, namespace string, services []*rpc.ServiceAssociation) {
	if len(services) == 0 {
		return
	}

	ingresses, err := listIngresses(ctx, namespace)
	if err != nil {
		clog.Warnf(ctx, "unable to discover ingresses in namespace %s: %v", namespace, err)
		return
	}

	index := serviceIndex(services)
	for _, ingress := range ingresses {
		attachRoutesForIngress(ingress, index)
	}

	count := 0
	for _, service := range services {
		sort.Slice(service.Routes, func(i, j int) bool {
			if service.Routes[i].GetName() != service.Routes[j].GetName() {
				return service.Routes[i].GetName() < service.Routes[j].GetName()
			}
			if service.Routes[i].GetPort() != service.Routes[j].GetPort() {
				return service.Routes[i].GetPort() < service.Routes[j].GetPort()
			}
			return service.Routes[i].GetPortName() < service.Routes[j].GetPortName()
		})
		count += len(service.Routes)
	}
	clog.Debugf(ctx, "discovered %d routes in namespace %s for %d services", count, namespace, len(services))
}

func listIngresses(ctx context.Context, namespace string) ([]*netv1.Ingress, error) {
	if factory := informer.GetK8sFactory(ctx, namespace); factory != nil {
		return factory.Networking().V1().Ingresses().Lister().Ingresses(namespace).List(labels.Everything())
	}

	// This shouldn't happen really.
	clog.Debugf(ctx, "listing ingresses in namespace %s using direct API call", namespace)
	list, err := k8sapi.GetK8sInterface(ctx).NetworkingV1().Ingresses(namespace).List(ctx, meta.ListOptions{})
	if err != nil {
		return nil, err
	}

	ingresses := make([]*netv1.Ingress, 0, len(list.Items))
	for i := range list.Items {
		ingresses = append(ingresses, &list.Items[i])
	}
	return ingresses, nil
}

// attachRoutesForIngress appends a route to each service port that the given ingress
// routes traffic to. The hosts and paths of all ingress rules targeting the same
// service port are aggregated into one single route.
func attachRoutesForIngress(ingress *netv1.Ingress, services map[string]*rpc.ServiceAssociation) {
	if ingress == nil {
		return
	}

	type routeKey struct {
		service  *rpc.ServiceAssociation
		portName string
		port     int32
	}
	type routeAggregate struct {
		hosts map[string]struct{}
		paths map[string]struct{}
	}

	aggregates := map[routeKey]*routeAggregate{}

	appendMatch := func(service *rpc.ServiceAssociation, portName string, port int32, host string, path string) {
		if service == nil || service.GetName() == "" {
			return
		}
		key := routeKey{
			service:  service,
			portName: portName,
			port:     port,
		}
		aggregate := aggregates[key]
		if aggregate == nil {
			aggregate = &routeAggregate{
				hosts: map[string]struct{}{},
				paths: map[string]struct{}{},
			}
			aggregates[key] = aggregate
		}
		if host != "" {
			aggregate.hosts[host] = struct{}{}
		}
		if path != "" {
			aggregate.paths[path] = struct{}{}
		}
	}

	if service, portName, port, ok := ingressBackendMatch(ingress.Spec.DefaultBackend, services); ok {
		appendMatch(service, portName, port, "", "/")
	}

	for _, rule := range ingress.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, path := range rule.HTTP.Paths {
			service, portName, port, ok := ingressBackendMatch(&path.Backend, services)
			if !ok {
				continue
			}
			appendMatch(service, portName, port, rule.Host, path.Path)
		}
	}

	addresses := ingressAddresses(ingress)
	for key, aggregate := range aggregates {
		key.service.Routes = append(key.service.Routes, &rpc.RouteAssociation{
			Type:      "Ingress",
			Name:      ingress.Name,
			Hosts:     sortedKeys(aggregate.hosts),
			Paths:     sortedKeys(aggregate.paths),
			Tls:       ingressUsesTLS(ingress),
			PortName:  key.portName,
			Port:      key.port,
			Addresses: addresses,
		})
	}
}

// ingressAddresses returns the load-balancer addresses of the given ingress,
// preferring hostnames over IPs.
func ingressAddresses(ingress *netv1.Ingress) []string {
	lbs := ingress.Status.LoadBalancer.Ingress
	if len(lbs) == 0 {
		return nil
	}
	addresses := make([]string, 0, len(lbs))
	for _, lb := range lbs {
		switch {
		case lb.Hostname != "":
			addresses = append(addresses, lb.Hostname)
		case lb.IP != "":
			addresses = append(addresses, lb.IP)
		}
	}
	sort.Strings(addresses)
	return addresses
}

func ingressBackendMatch(
	backend *netv1.IngressBackend,
	services map[string]*rpc.ServiceAssociation,
) (*rpc.ServiceAssociation, string, int32, bool) {
	if backend == nil || backend.Service == nil {
		return nil, "", 0, false
	}
	service, ok := services[backend.Service.Name]
	if !ok || service == nil {
		return nil, "", 0, false
	}

	portName := backend.Service.Port.Name
	portNumber := backend.Service.Port.Number
	if portName == "" && portNumber == 0 && len(service.Ports) == 1 {
		portName = service.Ports[0].GetName()
		portNumber = service.Ports[0].GetPort()
	}
	if portName != "" {
		for _, port := range service.Ports {
			if port != nil && port.GetName() == portName {
				return service, portName, port.GetPort(), true
			}
		}
	}
	if portNumber != 0 {
		for _, port := range service.Ports {
			if port != nil && port.GetPort() == portNumber {
				return service, port.GetName(), portNumber, true
			}
		}
	}
	return service, portName, portNumber, true
}

func ingressUsesTLS(ingress *netv1.Ingress) bool {
	if ingress == nil {
		return false
	}
	if len(ingress.Spec.TLS) > 0 {
		return true
	}

	annotations := ingress.GetAnnotations()
	if len(annotations) == 0 {
		return false
	}

	for _, key := range []string{
		"networking.gke.io/managed-certificates",
		"ingress.gcp.kubernetes.io/pre-shared-cert",
		"ingress.kubernetes.io/https-forwarding-rule",
		"ingress.kubernetes.io/https-target-proxy",
		"ingress.kubernetes.io/ssl-cert",
	} {
		if annotations[key] != "" {
			return true
		}
	}
	return false
}

func serviceIndex(services []*rpc.ServiceAssociation) map[string]*rpc.ServiceAssociation {
	index := make(map[string]*rpc.ServiceAssociation, len(services))
	for _, service := range services {
		if service == nil || service.GetName() == "" {
			continue
		}
		index[service.GetName()] = service
	}
	return index
}

func sortedKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
