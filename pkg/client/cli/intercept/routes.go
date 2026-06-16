package intercept

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// routesForSpec returns the route associations for the service port that the
// given intercept spec targets.
func routesForSpec(spec *manager.InterceptSpec, workloads []*connector.WorkloadInfo) []*manager.RouteAssociation {
	for _, wl := range workloads {
		if wl.GetName() == spec.Agent && wl.GetNamespace() == spec.Namespace {
			return routesForService(spec, wl.GetServices())
		}
	}
	return nil
}

// routesForService returns the routes of the service named by the given intercept
// spec that target the service port that the spec identifies.
func routesForService(spec *manager.InterceptSpec, services []*manager.ServiceAssociation) []*manager.RouteAssociation {
	if spec.ServiceName == "" {
		return nil
	}
	for _, svc := range services {
		if svc.GetName() != spec.ServiceName {
			continue
		}
		var routes []*manager.RouteAssociation
		for _, route := range svc.GetRoutes() {
			if spec.ServicePort != 0 && route.GetPort() == spec.ServicePort ||
				spec.ServicePortName != "" && route.GetPortName() == spec.ServicePortName {
				routes = append(routes, route)
			}
		}
		return routes
	}
	return nil
}

// routeURLs builds one URL per host (or load-balancer address when the route
// declares no hosts) using the route's first path. The returned slice is sorted
// and free from duplicates.
func routeURLs(routes []*manager.RouteAssociation) []string {
	urls := make(map[string]struct{})
	for _, route := range routes {
		scheme := "http://"
		if route.GetTls() {
			scheme = "https://"
		}
		path := ""
		if paths := route.GetPaths(); len(paths) > 0 {
			path = paths[0]
		}
		if path == "/" {
			path = ""
		}
		hosts := route.GetHosts()
		if len(hosts) == 0 {
			hosts = route.GetAddresses()
		}
		for _, host := range hosts {
			urls[scheme+host+path] = struct{}{}
		}
	}
	if len(urls) == 0 {
		return nil
	}
	return slices.Sorted(maps.Keys(urls))
}

// curlExample returns a curl command that reaches the given URL with one -H flag
// for each of the given headers, or an empty string when the URL contains a
// wildcard host.
func curlExample(url string, headers map[string]string) string {
	if url == "" || strings.Contains(url, "*") {
		return ""
	}
	sb := strings.Builder{}
	sb.WriteString("curl")
	for _, k := range slices.Sorted(maps.Keys(headers)) {
		fmt.Fprintf(&sb, " -H '%s: %s'", k, headers[k])
	}
	sb.WriteByte(' ')
	sb.WriteString(url)
	return sb.String()
}
