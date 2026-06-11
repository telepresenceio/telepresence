package state

import (
	"context"
	"maps"
	"sort"

	netv1 "k8s.io/api/networking/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

type workloadExposure struct {
	desiredReplicas int32
	readyReplicas   int32
	services        []*rpc.ServiceAssociation
	routes          []*rpc.RouteAssociation
}

type serviceRouteKey struct {
	name      string
	namespace string
	portName  string
	port      int32
}

func describeWorkloadExposure(ctx context.Context, wl k8sapi.Workload) workloadExposure {
	services := serviceAssociationsForWorkload(ctx, wl)
	return workloadExposure{
		desiredReplicas: k8sapi.DesiredReplicas(wl),
		readyReplicas:   k8sapi.ReadyReplicas(wl),
		services:        services,
		routes:          routeAssociationsForServices(ctx, wl.GetNamespace(), serviceRouteIndex(services)),
	}
}

func serviceAssociationsForWorkload(ctx context.Context, wl k8sapi.Workload) []*rpc.ServiceAssociation {
	podTemplate := wl.GetPodTemplate().DeepCopy()
	if podTemplate.Namespace == "" {
		podTemplate.Namespace = wl.GetNamespace()
	}

	objects, err := agentmap.FindServicesForPod(ctx, podTemplate, "")
	if err != nil {
		clog.Warnf(ctx, "unable to discover services for %s %s.%s: %v", wl.GetKind(), wl.GetName(), wl.GetNamespace(), err)
		return nil
	}

	services := make([]*rpc.ServiceAssociation, 0, len(objects))
	for _, object := range objects {
		service, ok := k8sapi.ServiceImpl(object)
		if !ok {
			continue
		}

		ports := make([]*rpc.ServicePort, 0, len(service.Spec.Ports))
		for _, port := range service.Spec.Ports {
			ports = append(ports, &rpc.ServicePort{
				Name:       port.Name,
				Port:       port.Port,
				TargetPort: port.TargetPort.String(),
			})
		}
		sort.Slice(ports, func(i, j int) bool {
			if ports[i].GetPort() != ports[j].GetPort() {
				return ports[i].GetPort() < ports[j].GetPort()
			}
			return ports[i].GetName() < ports[j].GetName()
		})

		services = append(services, &rpc.ServiceAssociation{
			Name:      service.Name,
			Namespace: service.Namespace,
			Ports:     ports,
		})
	}

	sort.Slice(services, func(i, j int) bool {
		if services[i].GetNamespace() != services[j].GetNamespace() {
			return services[i].GetNamespace() < services[j].GetNamespace()
		}
		return services[i].GetName() < services[j].GetName()
	})
	clog.Debugf(ctx, "discovered %d services for %s %s.%s using pod labels %v", len(services), wl.GetKind(), wl.GetName(), wl.GetNamespace(), podTemplate.Labels)
	return services
}

func routeAssociationsForServices(ctx context.Context, namespace string, services map[string]*rpc.ServiceAssociation) []*rpc.RouteAssociation {
	if len(services) == 0 {
		clog.Debugf(ctx, "discovered 0 routes in namespace %s because no matching services were found", namespace)
		return nil
	}

	ingresses, err := listIngresses(ctx, namespace)
	if err != nil {
		clog.Warnf(ctx, "unable to discover ingresses in namespace %s: %v", namespace, err)
		return nil
	}

	routes := make([]*rpc.RouteAssociation, 0, len(ingresses))
	for _, ingress := range ingresses {
		routes = append(routes, routeAssociationsForIngress(ingress, services)...)
	}

	sort.Slice(routes, func(i, j int) bool {
		if routes[i].GetNamespace() != routes[j].GetNamespace() {
			return routes[i].GetNamespace() < routes[j].GetNamespace()
		}
		if routes[i].GetServiceNamespace() != routes[j].GetServiceNamespace() {
			return routes[i].GetServiceNamespace() < routes[j].GetServiceNamespace()
		}
		if routes[i].GetServiceName() != routes[j].GetServiceName() {
			return routes[i].GetServiceName() < routes[j].GetServiceName()
		}
		if routes[i].GetServicePort() != routes[j].GetServicePort() {
			return routes[i].GetServicePort() < routes[j].GetServicePort()
		}
		if routes[i].GetServicePortName() != routes[j].GetServicePortName() {
			return routes[i].GetServicePortName() < routes[j].GetServicePortName()
		}
		return routes[i].GetName() < routes[j].GetName()
	})
	clog.Debugf(ctx, "discovered %d routes in namespace %s for services %v", len(routes), namespace, maps.Keys(services))
	return routes
}

func listIngresses(ctx context.Context, namespace string) ([]*netv1.Ingress, error) {
	if factory := informer.GetK8sFactory(ctx, namespace); factory != nil {
		ingresses, err := factory.Networking().V1().Ingresses().Lister().Ingresses(namespace).List(labels.Everything())
		if err == nil && len(ingresses) > 0 {
			return ingresses, nil
		}
		if err != nil {
			clog.Debugf(ctx, "namespace ingress lister lookup failed for %s, falling back to direct client: %v", namespace, err)
		}
	}

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

func routeAssociationsForIngress(ingress *netv1.Ingress, services map[string]*rpc.ServiceAssociation) []*rpc.RouteAssociation {
	if ingress == nil {
		return nil
	}

	type routeAggregate struct {
		key   serviceRouteKey
		hosts map[string]struct{}
		paths map[string]struct{}
	}

	aggregates := map[serviceRouteKey]*routeAggregate{}

	appendMatch := func(service *rpc.ServiceAssociation, portName string, port int32, host string, path string) {
		if service == nil || service.GetName() == "" {
			return
		}
		key := serviceRouteKey{
			name:      service.GetName(),
			namespace: service.GetNamespace(),
			portName:  portName,
			port:      port,
		}
		aggregate := aggregates[key]
		if aggregate == nil {
			aggregate = &routeAggregate{
				key:   key,
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

	if len(aggregates) == 0 {
		return nil
	}

	routes := make([]*rpc.RouteAssociation, 0, len(aggregates))
	for _, aggregate := range aggregates {
		routes = append(routes, &rpc.RouteAssociation{
			Type:             "Ingress",
			Name:             ingress.Name,
			Namespace:        ingress.Namespace,
			Hosts:            sortedKeys(aggregate.hosts),
			Paths:            sortedKeys(aggregate.paths),
			Tls:              ingressUsesTLS(ingress),
			ServiceName:      aggregate.key.name,
			ServiceNamespace: aggregate.key.namespace,
			ServicePortName:  aggregate.key.portName,
			ServicePort:      aggregate.key.port,
		})
	}
	return routes
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

func serviceRouteIndex(services []*rpc.ServiceAssociation) map[string]*rpc.ServiceAssociation {
	if len(services) == 0 {
		return nil
	}
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
