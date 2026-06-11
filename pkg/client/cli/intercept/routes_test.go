package intercept

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func testServices() []*manager.ServiceAssociation {
	return []*manager.ServiceAssociation{
		{
			Name: "echo",
			Ports: []*manager.ServicePort{
				{Name: "http", Port: 80, TargetPort: "8080"},
				{Name: "grpc", Port: 8081, TargetPort: "grpc"},
			},
			Routes: []*manager.RouteAssociation{
				{
					Type:     "Ingress",
					Name:     "echo-ing",
					Hosts:    []string{"api.example.com"},
					Paths:    []string{"/api"},
					Tls:      true,
					PortName: "http",
					Port:     80,
				},
				{
					Type:     "Ingress",
					Name:     "echo-grpc-ing",
					Hosts:    []string{"grpc.example.com"},
					Tls:      false,
					PortName: "grpc",
					Port:     8081,
				},
			},
		},
		{
			Name: "other",
			Routes: []*manager.RouteAssociation{
				{Type: "Ingress", Name: "other-ing", Hosts: []string{"other.example.com"}, Port: 80},
			},
		},
	}
}

func TestRoutesForService(t *testing.T) {
	services := testServices()

	// Match by port number.
	routes := routesForService(&manager.InterceptSpec{ServiceName: "echo", ServicePort: 80}, services)
	assert.Len(t, routes, 1)
	assert.Equal(t, "echo-ing", routes[0].GetName())

	// Match by port name only.
	routes = routesForService(&manager.InterceptSpec{ServiceName: "echo", ServicePortName: "grpc"}, services)
	assert.Len(t, routes, 1)
	assert.Equal(t, "echo-grpc-ing", routes[0].GetName())

	// No service name.
	assert.Nil(t, routesForService(&manager.InterceptSpec{ServicePort: 80}, services))

	// Unknown service.
	assert.Nil(t, routesForService(&manager.InterceptSpec{ServiceName: "nope", ServicePort: 80}, services))

	// Known service, no matching port.
	assert.Nil(t, routesForService(&manager.InterceptSpec{ServiceName: "echo", ServicePort: 9999}, services))
}

func TestRoutesForSpec(t *testing.T) {
	workloads := []*connector.WorkloadInfo{
		{Name: "echo", Namespace: "default", Services: testServices()},
	}
	spec := &manager.InterceptSpec{Agent: "echo", Namespace: "default", ServiceName: "echo", ServicePort: 80}
	routes := routesForSpec(spec, workloads)
	assert.Len(t, routes, 1)

	// Wrong namespace.
	spec = &manager.InterceptSpec{Agent: "echo", Namespace: "other", ServiceName: "echo", ServicePort: 80}
	assert.Nil(t, routesForSpec(spec, workloads))
}

func TestRouteURLs(t *testing.T) {
	// TLS scheme, path appended.
	urls := routeURLs([]*manager.RouteAssociation{
		{Hosts: []string{"api.example.com"}, Paths: []string{"/api"}, Tls: true},
	})
	assert.Equal(t, []string{"https://api.example.com/api"}, urls)

	// Plain scheme, root path omitted.
	urls = routeURLs([]*manager.RouteAssociation{
		{Hosts: []string{"api.example.com"}, Paths: []string{"/"}},
	})
	assert.Equal(t, []string{"http://api.example.com"}, urls)

	// Host-less route falls back to load-balancer addresses.
	urls = routeURLs([]*manager.RouteAssociation{
		{Addresses: []string{"192.168.1.1"}, Paths: []string{"/api"}},
	})
	assert.Equal(t, []string{"http://192.168.1.1/api"}, urls)

	// Multiple hosts, sorted and de-duplicated.
	urls = routeURLs([]*manager.RouteAssociation{
		{Hosts: []string{"b.example.com", "a.example.com"}},
		{Hosts: []string{"a.example.com"}},
	})
	assert.Equal(t, []string{"http://a.example.com", "http://b.example.com"}, urls)

	// No hosts and no addresses yields nothing.
	assert.Nil(t, routeURLs([]*manager.RouteAssociation{{Paths: []string{"/api"}}}))
	assert.Nil(t, routeURLs(nil))
}

func TestCurlExample(t *testing.T) {
	assert.Equal(t,
		"curl -H 'a: b' -H 'x: y' https://api.example.com/api",
		curlExample("https://api.example.com/api", map[string]string{"x": "y", "a": "b"}))
	assert.Equal(t, "curl http://api.example.com", curlExample("http://api.example.com", nil))

	// Wildcard hosts are not pasteable.
	assert.Equal(t, "", curlExample("https://*.example.com", map[string]string{"x": "y"}))
	assert.Equal(t, "", curlExample("", map[string]string{"x": "y"}))
}
