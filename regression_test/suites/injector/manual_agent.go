package injector

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// manualAgentName is both the Deployment/Service name this suite hand-builds.
const manualAgentName = "manual-agent"

// manualAgentServiceManifest/manualAgentDeploymentManifest are a bare
// workload with no telepresence involvement at all. The Service is applied
// to the cluster up front (genyaml's config step reads it live to build the
// agent config); the Deployment is kept local and used only as genyaml's
// --input, so the cluster only ever sees the already-agent-carrying version
// this suite applies at the end.
const manualAgentServiceManifest = `apiVersion: v1
kind: Service
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  selector:
    app: %[1]s
  ports:
    - port: 80
`

const manualAgentDeploymentManifest = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[1]s
  namespace: %[2]s
  labels:
    app: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %[1]s
  template:
    metadata:
      labels:
        app: %[1]s
    spec:
      containers:
        - name: echo
          image: ghcr.io/telepresenceio/echo-server:0.3.1
          imagePullPolicy: IfNotPresent
          ports:
            - containerPort: 80
`

// ManualAgent proves that an agent built by hand from telepresence genyaml's
// output (config, container, initcontainer, volume, annotations) and patched
// into a workload's manifest intercepts with no webhook involved at all: the
// shared release keeps the injector disabled throughout.
//
// The Service's one port is left unnamed, so Kubernetes defaults its
// targetPort to the same numeric value as port: agentconfig marks that
// intercept TargetPortNumeric (pkg/agentmap/generator.go), which is what
// makes genyaml's initcontainer step (pkg/client/cli/cmd/genyaml.go's
// genInitContainerInfo.run) produce an init container instead of erroring
// that none is needed. It also means the app container needs no port
// renamed before the agent's own "http"-named port is appended, since the
// service targets the port by number rather than by an "http" name.
type ManualAgent struct {
	rt.Suite
}

func init() {
	rt.Register(&ManualAgent{}, rt.InArea("injector"), rt.NeedsManager(managers.InjectorDisabled()))
}

func (s *ManualAgent) Test_HandBuiltAgentIntercepts() {
	t := s.T()
	ctx := s.Ctx()
	env := rt.Env{Ctx: ctx, T: t, R: s.R()}
	ns := rt.PrivateNamespace(env, "manual-agent")
	// ns is brand new: the already-running manager only manages it once its
	// pod restarts and re-lists namespaces.
	s.Require().NoError(rt.RestartManager(env))
	conn := switchManagerSpec(t, ctx, managers.InjectorDisabled(), ns)

	dir := s.R().ArtifactDir("manual-agent")
	svcPath := filepath.Join(dir, "service.yaml")
	writeFile(t, svcPath, fmt.Sprintf(manualAgentServiceManifest, manualAgentName, ns))
	if _, err := s.R().Kubectl(ctx, ns, "apply", "-f", svcPath); err != nil {
		t.Fatalf("applying service: %v", err)
	}

	deployPath := filepath.Join(dir, "deployment.yaml")
	writeFile(t, deployPath, fmt.Sprintf(manualAgentDeploymentManifest, manualAgentName, ns))

	agentImage := fmt.Sprintf("%s/tel2:%s", s.R().Registry(), s.R().ManagerVersion().String())
	cfgYAML := s.genYAML(t, "config",
		"--agent-image", agentImage,
		"--manager-namespace", managers.ManagerNamespace,
		"--namespace", ns,
		"--input", deployPath)
	cfgPath := filepath.Join(dir, "agent-config.yaml")
	writeFile(t, cfgPath, cfgYAML)

	var agentContainer corev1.Container
	unmarshalYAML(t, s.genYAML(t, "container", "--agent", cfgPath, "--input", deployPath), &agentContainer)

	var initContainer corev1.Container
	unmarshalYAML(t, s.genYAML(t, "initcontainer", "--agent", cfgPath), &initContainer)

	// --agent/--input are accepted but currently unused by genyaml volume
	// (pkg/client/cli/cmd/genyaml.go's genVolumeInfo.run calls
	// agentconfig.AgentVolumes(g.workloadName, nil), and g.workloadName is
	// never populated on this path): the volume list it returns is the same
	// four static volumes regardless of workload, so no flags are passed.
	var volumes []corev1.Volume
	unmarshalYAML(t, s.genYAML(t, "volume"), &volumes)

	var anns map[string]string
	unmarshalYAML(t, s.genYAML(t, "annotations", "--agent", cfgPath), &anns)

	var dep appsv1.Deployment
	depYAML, err := os.ReadFile(deployPath)
	if err != nil {
		t.Fatalf("reading deployment manifest: %v", err)
	}
	unmarshalYAML(t, string(depYAML), &dep)

	dep.Spec.Template.Spec.Containers = append(dep.Spec.Template.Spec.Containers, agentContainer)
	dep.Spec.Template.Spec.InitContainers = append(dep.Spec.Template.Spec.InitContainers, initContainer)
	dep.Spec.Template.Spec.Volumes = append(dep.Spec.Template.Spec.Volumes, volumes...)
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = map[string]string{}
	}
	for k, v := range anns {
		dep.Spec.Template.Annotations[k] = v
	}

	mergedYAML, err := yaml.Marshal(&dep)
	if err != nil {
		t.Fatalf("marshaling patched deployment: %v", err)
	}
	mergedPath := filepath.Join(dir, "deployment-with-agent.yaml")
	writeFile(t, mergedPath, string(mergedYAML))
	if _, err := s.R().Kubectl(ctx, ns, "apply", "-f", mergedPath); err != nil {
		t.Fatalf("applying agent-carrying deployment: %v", err)
	}
	if _, err := s.R().Kubectl(ctx, ns, "rollout", "status", "deploy/"+manualAgentName, "--timeout=120s"); err != nil {
		t.Fatalf("rollout: %v", err)
	}

	wl := &rt.Workload{Name: manualAgentName, Namespace: ns, Kind: "Deployment", Port: 80, SvcName: manualAgentName}
	ls := s.LocalEcho()
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "80"), cli.MountFalse())
	defer a.Detach(t)
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
}

// genYAML runs `telepresence genyaml <sub> --output - <extra...>` and
// returns stdout, failing t on error.
func (s *ManualAgent) genYAML(t testing.TB, sub string, extra ...string) string {
	t.Helper()
	args := append([]string{"genyaml", sub, "--output", "-"}, extra...)
	stdout, stderr, err := s.CLI().Run(s.Ctx(), args...)
	if err != nil {
		t.Fatalf("telepresence %s: %v\nstderr:\n%s", strings.Join(args, " "), err, stderr)
	}
	return stdout
}

func writeFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func unmarshalYAML(t testing.TB, data string, out any) {
	t.Helper()
	if err := yaml.Unmarshal([]byte(data), out); err != nil {
		t.Fatalf("unmarshaling %T: %v\ndata:\n%s", out, err, data)
	}
}
