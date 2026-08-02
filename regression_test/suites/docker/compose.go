package docker

import (
	"fmt"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/compose"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

const (
	// composeToolImage is the generic "do nothing forever" container image
	// every connect/proxy/ingest/dns compose service in this file uses.
	composeToolImage = "busybox"

	// composeEchoImage is the local handler image for intercept/replace/
	// wiretap tests: a tiny HTTP server whose response text is fully
	// controlled by -text, so a wget from a "tester" connect container can
	// distinguish it from the cluster's own echo-server response.
	composeEchoImage = "hashicorp/http-echo"

	// composeEchoMarker is the fixed response text composeEchoImage serves.
	composeEchoMarker = "rtest-compose-marker"

	// composeLocalHTTPPort is the port composeEchoImage listens on inside
	// its own compose container, and the local half of every intercept/
	// wiretap Ports mapping in this file: a compose-internal container
	// port, isolated per test and carrying no cluster meaning.
	composeLocalHTTPPort = 80

	// composeReplaceContainerPort is the remote half of a replace Ports
	// mapping (docs/reference/compose.md#replace: "<local
	// port>:<container port>"): workloads.Echo's own fixed container port,
	// the echo-server image's own listening port.
	composeReplaceContainerPort = 8080

	// composeConnectTimeout/Interval bound a connect/proxy/dns assertion:
	// no traffic-agent injection is involved, so cluster DNS/routing
	// settles quickly.
	composeConnectTimeout  = 30 * time.Second
	composeConnectInterval = 3 * time.Second

	// composeAttachTimeout/Interval bound an ingest/intercept/replace/
	// wiretap assertion: these wait on traffic-agent injection plus the
	// attachment itself, both slower than a plain connect.
	composeAttachTimeout  = 60 * time.Second
	composeAttachInterval = 5 * time.Second
)

// composeSleepInfinityCmd is the standing command every "do nothing
// forever" compose service in this file runs.
func composeSleepInfinityCmd() []string { return []string{"sleep", "infinity"} }

// composeEchoArgs is composeEchoImage's invocation, serving composeEchoMarker
// on composeLocalHTTPPort.
func composeEchoArgs() []string {
	return []string{"-text=" + composeEchoMarker, fmt.Sprintf("-listen=:%d", composeLocalHTTPPort)}
}

// Compose is one test per `x-tele` service-extension verb, each a
// builder-generated project (framework/compose) brought up with
// `telepresence compose up -d`, polled through `telepresence compose exec`,
// and torn down with `compose down`. Supersedes compose_test.go's
// Test_Compose{DNS,Connect,Proxy,Ingest,Intercept,Replace,Wiretap}.
type Compose struct {
	rt.Suite
}

func init() {
	rt.Register(&Compose{},
		rt.InArea("docker"),
		rt.NeedsManager(managers.Default),
		rt.Requires(rt.Docker),
		rt.On("linux"),
	)
}

// connection is a single-entry x-tele.connections block targeting ns: every
// test in this file uses exactly one (unnamed) connection.
func (s *Compose) connection(ns string) compose.Connection {
	return compose.Connection{Namespace: ns, ManagerNamespace: managers.ManagerNamespace}
}

// composeUp writes proj under ArtifactDir("compose"), brings it up, and
// registers a `compose down` cleanup, returning the compose file's path for
// the caller's own `compose exec`/`compose logs` polling.
func (s *Compose) composeUp(proj *compose.Project) string {
	t := s.T()
	ctx := s.Ctx()
	path, err := proj.Write(rt.Env{Ctx: ctx, T: t, R: s.R()})
	s.Require().NoError(err, "writing compose project")

	t.Cleanup(func() { _, _, _ = s.CLI().Run(ctx, "compose", "-f", path, "down") })
	_, stderr, err := s.CLI().Run(ctx, "compose", "-f", path, "up", "-d")
	s.Require().NoError(err, "compose up: %s", stderr)
	return path
}

// composeExec runs `telepresence compose -f path exec service args...` and
// reports its stdout/stderr/error.
func (s *Compose) composeExec(path, service string, args ...string) (stdout, stderr string, err error) {
	execArgs := append([]string{"compose", "-f", path, "exec", service}, args...)
	return s.CLI().Run(s.Ctx(), execArgs...)
}

// Test_Connect proves a `type: connect` compose service reaches a cluster
// service by its namespace-qualified, port-qualified URL:
// compose_test.go's Test_ComposeConnect used a bare name against a Service
// it created on port 80 (s.ApplyEchoService(ctx, svc, 80)); workloads.Echo
// always exposes port 8080 (no port-80 override), so this uses wl.
// ServiceURL() rather than a bare "http://"+wl.SvcName, for two reasons
// live-tested independently: (1) the port - a bare URL defaults to 80,
// which workloads.Echo's Service never listens on, so kube-proxy has no
// DNAT rule for it and the connection blackholes (confirmed with `wget
// $clusterIP:80` timing out while `wget $clusterIP:8080` succeeds
// instantly, from both outside and inside telepresence); and (2) the
// namespace qualification - a bare single-label query that needs
// cluster-side resolution (pkg/client/rootd/session.go's simpleLookup) is
// answered by ags.GetRandomAgent, any currently-tunneled agent, not
// necessarily one in wl.Namespace; that agent resolves the name with its
// own pod's resolver, so a bare name only works when the picked agent
// happens to share wl.Namespace. This suite's connection accumulates
// agents across tests (compose-ingest, compose-intercept, ...) and
// dev-mode adopts agents left running by unrelated suites in other
// namespaces (e.g. injector's manual-agent), so that assumption doesn't
// hold; a namespace-qualified name resolves the same way regardless of
// which agent answers, because every pod's resolv.conf carries the
// namespace-independent "svc.cluster.local" search entry.
func (s *Compose) Test_Connect() {
	t := s.T()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("compose-connect"))
	url := wl.ServiceURL()

	proj := &compose.Project{
		Connections: []compose.Connection{s.connection(ns)},
		Services: map[string]*compose.Service{
			"tester": {XTele: compose.Connect{}, Image: composeToolImage, Command: composeSleepInfinityCmd()},
		},
	}
	path := s.composeUp(proj)

	s.Eventually(func() bool {
		stdout, stderr, err := s.composeExec(path, "tester", "wget", "-qO-", url)
		if err == nil && len(stdout) > 0 {
			return true
		}
		t.Logf("wget %s: %v\nstderr:\n%s", url, err, stderr)
		return false
	}, composeConnectTimeout, composeConnectInterval, "wget of %s from connect container should succeed", url)
}

// Test_Proxy proves a `type: proxy` compose service is replaced by a proxy
// redirecting to the cluster service of the same name: compose_test.go's
// Test_ComposeProxy. wl.ServiceURL(): see Test_Connect's doc comment (the
// proxy's own tester side-car is a plain `type: connect` service, subject
// to the same port and GetRandomAgent-namespace issues). Proxy.Name is set
// namespace-qualified for the same GetRandomAgent reason: activating a
// proxy attachment itself resolves this name (pkg/client/cli/docker/
// compose/connection.go's resolveProxies), and a bare name defaulting to
// the compose service's own key would hit the identical bare-single-label
// lookup live-tested to fail in Test_Connect - live-tested here too
// (`telepresence compose up` itself failed with "unable to resolve name
// compose-proxy" before this field was set).
func (s *Compose) Test_Proxy() {
	t := s.T()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("compose-proxy"))
	url := wl.ServiceURL()

	proj := &compose.Project{
		Connections: []compose.Connection{s.connection(ns)},
		Services: map[string]*compose.Service{
			wl.Name:  {XTele: compose.Proxy{Name: wl.SvcName + "." + wl.Namespace}},
			"tester": {XTele: compose.Connect{}, Image: composeToolImage, Command: composeSleepInfinityCmd()},
		},
	}
	path := s.composeUp(proj)

	s.Eventually(func() bool {
		stdout, stderr, err := s.composeExec(path, "tester", "wget", "-qO-", url)
		if err == nil && len(stdout) > 0 {
			return true
		}
		t.Logf("wget via proxy %s: %v\nstderr:\n%s", url, err, stderr)
		return false
	}, composeConnectTimeout, composeConnectInterval, "wget of %s through the proxy should succeed", url)
}

// composeIngestEnvVar/composeIngestEnvValue is a declared container env var
// Test_Ingest asserts an ingest container inherits.
const (
	composeIngestEnvVar   = "RTEST_INGEST_VAR"
	composeIngestEnvValue = "rtest-ingest-marker"
)

// Test_Ingest proves a `type: ingest` compose service inherits the remote
// container's own declared environment: compose_test.go's
// Test_ComposeIngest, which asserted a bespoke INGEST_TEST_VAR. This
// originally asserted HOSTNAME instead, on the assumption that every pod
// carries it; live-testing showed the ingest env only mirrors a
// container's own declared spec.containers[].env (and, incidentally, the
// service-discovery vars kubelet injects for existing Services), never
// HOSTNAME or other purely kubelet/container-runtime-set vars, so a
// declared var (workloads.Template.Env, rendered like PORTS) is asserted
// instead, matching the itest's own approach.
func (s *Compose) Test_Ingest() {
	t := s.T()
	ns := s.AppNamespace()
	tpl := workloads.Echo("compose-ingest")
	tpl.Env = map[string]string{composeIngestEnvVar: composeIngestEnvValue}
	wl := s.Workload(tpl)

	proj := &compose.Project{
		Connections: []compose.Connection{s.connection(ns)},
		Services: map[string]*compose.Service{
			wl.Name: {XTele: compose.Ingest{}, Image: composeToolImage, Command: composeSleepInfinityCmd()},
		},
	}
	path := s.composeUp(proj)

	want := composeIngestEnvVar + "=" + composeIngestEnvValue
	s.Eventually(func() bool {
		stdout, stderr, err := s.composeExec(path, wl.Name, "env")
		if err == nil && strings.Contains(stdout, want) {
			return true
		}
		t.Logf("env: %v\nstderr:\n%s", err, stderr)
		return false
	}, composeAttachTimeout, composeAttachInterval, "ingest container should inherit %s from the cluster pod's own env", want)
}

// Test_Intercept proves a `type: intercept` compose service receives
// cluster traffic destined for the workload it names: compose_test.go's
// Test_ComposeIntercept.
func (s *Compose) Test_Intercept() {
	t := s.T()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("compose-intercept"))

	proj := &compose.Project{
		Connections: []compose.Connection{s.connection(ns)},
		Services: map[string]*compose.Service{
			wl.Name: {
				XTele:   compose.Intercept{Ports: []string{fmt.Sprintf("%d:http", composeLocalHTTPPort)}},
				Image:   composeEchoImage,
				Command: composeEchoArgs(),
			},
			"tester": {XTele: compose.Connect{}, Image: composeToolImage, Command: composeSleepInfinityCmd()},
		},
	}
	path := s.composeUp(proj)

	s.Eventually(func() bool {
		stdout, stderr, err := s.composeExec(path, "tester", "wget", "-qO-", "http://"+wl.SvcName)
		if err == nil && strings.Contains(stdout, composeEchoMarker) {
			return true
		}
		t.Logf("wget intercept: %v\nstderr:\n%s", err, stderr)
		return false
	}, composeAttachTimeout, composeAttachInterval,
		"intercept should redirect cluster traffic for %s to the compose container", wl.SvcName)
}

// Test_Replace proves a `type: replace` compose service serves cluster
// traffic in place of the workload's own container: compose_test.go's
// Test_ComposeReplace.
func (s *Compose) Test_Replace() {
	t := s.T()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("compose-replace"))

	proj := &compose.Project{
		Connections: []compose.Connection{s.connection(ns)},
		Services: map[string]*compose.Service{
			wl.Name: {
				XTele: compose.Replace{
					Ports: []string{fmt.Sprintf("%d:%d", composeLocalHTTPPort, composeReplaceContainerPort)},
				},
				Image:   composeEchoImage,
				Command: composeEchoArgs(),
			},
			"tester": {XTele: compose.Connect{}, Image: composeToolImage, Command: composeSleepInfinityCmd()},
		},
	}
	path := s.composeUp(proj)

	s.Eventually(func() bool {
		stdout, stderr, err := s.composeExec(path, "tester", "wget", "-qO-", "http://"+wl.SvcName)
		if err == nil && strings.Contains(stdout, composeEchoMarker) {
			return true
		}
		t.Logf("wget replace: %v\nstderr:\n%s", err, stderr)
		return false
	}, composeAttachTimeout, composeAttachInterval, "replace should serve traffic for %s from the compose container", wl.SvcName)
}

// Test_Wiretap proves a `type: wiretap` compose service receives copies of
// cluster traffic while the cluster's own container keeps serving it:
// compose_test.go's Test_ComposeWiretap. wl.ServiceURL(): see
// Test_Connect's doc comment for the port issue; the namespace
// qualification it also provides is required here for its own reason too
// - Docker's embedded DNS would otherwise resolve a bare service name to
// the local wiretap-receiver container itself, bypassing the cluster
// entirely.
func (s *Compose) Test_Wiretap() {
	t := s.T()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("compose-wiretap"))
	url := wl.ServiceURL()

	proj := &compose.Project{
		Connections: []compose.Connection{s.connection(ns)},
		Services: map[string]*compose.Service{
			wl.Name: {
				XTele:   compose.Wiretap{Ports: []string{fmt.Sprintf("%d:http", composeLocalHTTPPort)}},
				Image:   composeEchoImage,
				Command: composeEchoArgs(),
			},
			"tester": {XTele: compose.Connect{}, Image: composeToolImage, Command: composeSleepInfinityCmd()},
		},
	}
	path := s.composeUp(proj)

	s.Eventually(func() bool {
		stdout, stderr, err := s.composeExec(path, "tester", "wget", "-qO-", url)
		if err == nil && len(stdout) > 0 && !strings.Contains(stdout, composeEchoMarker) {
			return true
		}
		t.Logf("wget wiretap cluster check: %v\nstderr:\n%s", err, stderr)
		return false
	}, composeAttachTimeout, composeAttachInterval,
		"cluster service %s should still serve its own traffic during wiretap", url)

	for range 3 {
		_, _, _ = s.composeExec(path, "tester", "wget", "-qO-", url)
	}

	s.Eventually(func() bool {
		stdout, stderr, err := s.CLI().Run(s.Ctx(), "compose", "-f", path, "logs", wl.Name)
		if err == nil && strings.Contains(stdout, "GET") {
			return true
		}
		t.Logf("compose logs wiretap: %v\nstderr:\n%s", err, stderr)
		return false
	}, composeConnectTimeout, composeConnectInterval, "wiretap container should receive copies of traffic to %s", url)
}

// Test_DNS proves cluster DNS resolves from within a `type: replace`
// compose container: compose_test.go's Test_ComposeDNS.
func (s *Compose) Test_DNS() {
	t := s.T()
	ns := s.AppNamespace()
	target := s.Workload(workloads.Echo("compose-dns-target"))
	replaced := s.Workload(workloads.Echo("compose-dns-replaced"))

	proj := &compose.Project{
		Connections: []compose.Connection{s.connection(ns)},
		Services: map[string]*compose.Service{
			replaced.Name: {XTele: compose.Replace{}, Image: composeToolImage, Command: composeSleepInfinityCmd()},
		},
	}
	path := s.composeUp(proj)

	s.Eventually(func() bool {
		stdout, stderr, err := s.composeExec(path, replaced.Name, "nslookup", target.SvcName)
		if err == nil && strings.Contains(stdout, "Address:") {
			return true
		}
		t.Logf("nslookup: %v\nstderr:\n%s", err, stderr)
		return false
	}, composeAttachTimeout, composeAttachInterval, "nslookup of %s from the replace container should succeed", target.SvcName)
}
