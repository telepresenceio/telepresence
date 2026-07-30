// Package workloads renders test workload manifests (Deployment/StatefulSet +
// Service) from embedded templates.
package workloads

import (
	"bytes"
	"embed"
	"fmt"
	"strconv"
	"strings"
	"text/template"
)

//go:embed templates/*.yaml
var templatesFS embed.FS

const (
	// echoImage is the test echo-server image; it always listens on echoPort.
	echoImage = "ghcr.io/telepresenceio/echo-server:0.3.1"
	echoPort  = 8080
)

// Template describes a workload manifest to render.
type Template struct {
	Name     string
	Kind     string // "Deployment" or "StatefulSet"
	Replicas int
	Image    string
	Port     int32
	SvcName  string
	// Headless sets the Service's clusterIP to None (StatefulSet variants).
	Headless bool
	// NoService omits the Service object: the workload is reachable only via
	// pod IP/container port.
	NoService bool
	// ExtraPorts are additional named container/service ports beyond Port
	// ("http"), served by the same container via the echo-server's PORTS
	// env var (EchoMultiPort).
	ExtraPorts []NamedPort
	// Annotations are rendered onto the pod template's metadata.annotations,
	// e.g. telepresence.io/inject-container-ports for no-service workloads.
	Annotations map[string]string
	// Resources sets the app container's resource requests/limits, e.g. for
	// LimitRange-default assertions. Zero value renders no resources block.
	Resources Resources
	// AppProtocol sets appProtocol on the service's "http" port (e.g.
	// "kubernetes.io/h2c"), so the agent preserves the port's application
	// protocol instead of treating it as opaque TCP. Empty renders no
	// appProtocol field. Ignored when NoService is set, since there is no
	// service port to annotate.
	AppProtocol string
}

// NamedPort is an additional container/service port beyond Template.Port.
type NamedPort struct {
	Name string
	Port int32
}

// Resources is a workload's app container resources.requests/limits,
// rendered in Kubernetes quantity syntax (e.g. "100m", "64Mi"). A field left
// empty is omitted from the rendered manifest.
type Resources struct {
	Requests ResourceQuantities
	Limits   ResourceQuantities
}

// ResourceQuantities is a cpu/memory pair, as used by Resources.Requests and
// Resources.Limits.
type ResourceQuantities struct {
	CPU    string
	Memory string
}

// IsZero reports whether r sets no requests and no limits, used by the
// templates to skip rendering an empty resources block.
func (r Resources) IsZero() bool {
	return r == Resources{}
}

// PortsEnv is the echo-server PORTS env var value for a multi-port
// template: Port followed by each ExtraPorts entry, comma-separated.
// Render emits it only when ExtraPorts is non-empty.
func (t Template) PortsEnv() string {
	ports := make([]string, 0, 1+len(t.ExtraPorts))
	ports = append(ports, strconv.Itoa(int(t.Port)))
	for _, p := range t.ExtraPorts {
		ports = append(ports, strconv.Itoa(int(p.Port)))
	}
	return strings.Join(ports, ",")
}

// Echo returns a single-replica Deployment+Service template running the
// echo-server test image on one HTTP port.
func Echo(name string) Template {
	return Template{
		Name:     name,
		Kind:     "Deployment",
		Replicas: 1,
		Image:    echoImage,
		Port:     echoPort,
		SvcName:  name,
	}
}

// EchoStatefulSet is Echo, rendered as a StatefulSet instead of a Deployment.
func EchoStatefulSet(name string) Template {
	t := Echo(name)
	t.Kind = "StatefulSet"
	return t
}

// EchoHeadless is EchoStatefulSet with a headless (clusterIP: None) service,
// exercising per-pod StatefulSet DNS.
func EchoHeadless(name string) Template {
	t := EchoStatefulSet(name)
	t.Headless = true
	return t
}

// EchoNoService is Echo without a Service: the workload is reachable only
// via pod IP/container port, exercising no-service attach paths. It sets
// the inject-container-ports annotation, required for a service-less
// workload to be interceptable (pkg/annotation/annotation.go).
func EchoNoService(name string) Template {
	t := Echo(name)
	t.NoService = true
	t.SvcName = ""
	t.Annotations = map[string]string{"telepresence.io/inject-container-ports": "http"}
	return t
}

// EchoMultiPort is Echo with a second named HTTP port ("http2") on the same
// pod, both served by the one echo-server container.
func EchoMultiPort(name string) Template {
	t := Echo(name)
	t.ExtraPorts = []NamedPort{{Name: "http2", Port: echoPort + 1}}
	return t
}

// EchoReplicas is Echo scaled to n replicas.
func EchoReplicas(name string, n int) Template {
	t := Echo(name)
	t.Replicas = n
	return t
}

// Render executes the embedded template matching t.Kind in namespace and
// returns the resulting manifest YAML.
func (t Template) Render(namespace string) (string, error) {
	var file string
	switch t.Kind {
	case "Deployment":
		file = "templates/deployment.yaml"
	case "StatefulSet":
		file = "templates/statefulset.yaml"
	default:
		return "", fmt.Errorf("workloads: unknown kind %q", t.Kind)
	}
	tpl, err := template.ParseFS(templatesFS, file)
	if err != nil {
		return "", err
	}
	data := struct {
		Template
		Namespace string
	}{t, namespace}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
