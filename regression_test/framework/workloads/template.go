// Package workloads renders test workload manifests
// (Deployment/ReplicaSet/StatefulSet/Rollout + Service) from embedded
// templates.
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

	// udpEchoImage is the UDP-echo test image; it always listens on
	// udpEchoPort/UDP. UDPEcho keeps both the service and target port at
	// udpEchoPort, since nothing depends on a distinct external port here.
	udpEchoImage = "ghcr.io/telepresenceio/udp-echo:latest"
	udpEchoPort  = 8080
)

// Template describes a workload manifest to render.
type Template struct {
	Name     string
	Kind     string // "Deployment", "ReplicaSet", "StatefulSet", or "Rollout"
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
	// ConfigVolume, when non-zero, adds a ConfigMap (rendered as its own
	// manifest document ahead of the workload) and mounts it read-only into
	// the app container. Set by EchoWithConfigVolume; zero value renders no
	// ConfigMap and no extra volume.
	ConfigVolume ConfigVolume
	// UDP marks the primary port ("http" by default) as UDP instead of TCP.
	// Set by UDPEcho; ExtraPorts stay TCP-only, since no template currently
	// needs a mixed-protocol workload.
	UDP bool
	// Env are additional declared container env vars, name to value. Unlike
	// PortsEnv's PORTS var (implied by ExtraPorts), these are opt-in: a
	// suite that needs to assert on a workload's own declared env (as
	// opposed to a kubelet/container-runtime-injected var such as
	// HOSTNAME, which telepresence ingest never captures, since it only
	// mirrors a container's declared env) sets this directly. Rendered
	// after PORTS, sorted by key for a deterministic manifest.
	Env map[string]string
	// ExtraContainers are additional containers sharing the pod with the
	// app container, each running the echo image on its own port. Rendered
	// only for Deployment and ReplicaSet kinds (the shared deployment.yaml
	// template); ignored for StatefulSet and Rollout, whose templates carry
	// no equivalent block, since no suite needs a multi-container workload
	// of those kinds yet.
	ExtraContainers []ExtraContainer
}

// PortName is the primary port's name: "udp" when UDP is set (naming it
// "http" would misdescribe the protocol), "http" otherwise.
func (t Template) PortName() string {
	if t.UDP {
		return "udp"
	}
	return "http"
}

// ConfigVolume describes a ConfigMap a Template mounts as a volume: a
// ConfigMap object holding one key/value pair, mounted at a directory in
// the app container. The mounted file's name is Key and its content is
// Content.
type ConfigVolume struct {
	Name      string // ConfigMap object name
	Key       string // ConfigMap data key; also the mounted file's name
	Content   string // ConfigMap data value; also the mounted file's content
	MountPath string // directory the volume is mounted at
}

// IsZero reports whether v is the zero value, used by Render to skip
// emitting a ConfigMap and volume mount.
func (v ConfigVolume) IsZero() bool {
	return v == ConfigVolume{}
}

const (
	// ConfigVolumeMountPath is the well-known directory EchoWithConfigVolume
	// mounts its ConfigMap volume at in the app container (and, once
	// intercepted with mounts enabled, under the local FUSE/SFTP mount
	// root).
	ConfigVolumeMountPath = "/etc/rtest-config"
	// ConfigVolumeFileName is the ConfigMap data key, which is also the
	// mounted file's name: <mount root><ConfigVolumeMountPath>/<ConfigVolumeFileName>.
	ConfigVolumeFileName = "rtest.conf"
	// ConfigVolumeContent is the fixed, distinctive content suites assert on
	// after reading the mounted file. It contains no trailing newline: a
	// ConfigMap value is stored and mounted byte-for-byte, so the file's
	// content is exactly this string.
	ConfigVolumeContent = "rtest-config-volume-marker"
)

// ExtraContainer is an additional container in a Deployment/ReplicaSet pod,
// alongside the app container: the same echo image, listening on Port (via
// the same PORTS env mechanism Template's own ports use), its own Env vars,
// and, when ConfigVolume is non-zero, its own ConfigMap and read-only mount.
type ExtraContainer struct {
	Name         string
	Port         int32
	Env          map[string]string
	ConfigVolume ConfigVolume
}

// VolumeName is the pod volume name c's ConfigVolume mounts under: distinct
// per container name, so the app container's own "rtest-config" volume and
// other ExtraContainers' volumes never collide.
func (c ExtraContainer) VolumeName() string {
	return "rtest-config-" + c.Name
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

// HasExtraConfigVolumes reports whether any ExtraContainers entry sets
// ConfigVolume, used by deployment.yaml to decide whether to open the pod's
// volumes: block even when the app container's own ConfigVolume is unset.
func (t Template) HasExtraConfigVolumes() bool {
	for _, c := range t.ExtraContainers {
		if !c.ConfigVolume.IsZero() {
			return true
		}
	}
	return false
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

// EchoReplicaSet is Echo, rendered as a bare ReplicaSet (apps/v1, no
// rollout controller of its own) instead of a Deployment.
func EchoReplicaSet(name string) Template {
	t := Echo(name)
	t.Kind = "ReplicaSet"
	return t
}

// EchoRollout is Echo, rendered as an Argo Rollout (argoproj.io/v1alpha1)
// with an empty canary strategy instead of a Deployment.
func EchoRollout(name string) Template {
	t := Echo(name)
	t.Kind = "Rollout"
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

// EchoWithConfigVolume is Echo with an additional ConfigMap (named
// name+"-config") mounted read-only at ConfigVolumeMountPath: the ConfigMap
// holds one key, ConfigVolumeFileName, whose value is ConfigVolumeContent.
// Mounts-area suites intercept it and assert the local mount surfaces that
// file unchanged.
func EchoWithConfigVolume(name string) Template {
	t := Echo(name)
	t.ConfigVolume = ConfigVolume{
		Name:      name + "-config",
		Key:       ConfigVolumeFileName,
		Content:   ConfigVolumeContent,
		MountPath: ConfigVolumeMountPath,
	}
	return t
}

// UDPEcho returns a single-replica Deployment+Service template running the
// UDP-echo test image, the quic area's Datagrams test's UDP round-trip
// target. Unlike Echo, the Service exposes a UDP port.
func UDPEcho(name string) Template {
	return Template{
		Name:     name,
		Kind:     "Deployment",
		Replicas: 1,
		Image:    udpEchoImage,
		Port:     udpEchoPort,
		SvcName:  name,
		UDP:      true,
	}
}

// Render executes the embedded template matching t.Kind in namespace and
// returns the resulting manifest YAML.
func (t Template) Render(namespace string) (string, error) {
	var file string
	switch t.Kind {
	case "Deployment", "ReplicaSet":
		file = "templates/deployment.yaml"
	case "StatefulSet":
		file = "templates/statefulset.yaml"
	case "Rollout":
		file = "templates/rollout.yaml"
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
