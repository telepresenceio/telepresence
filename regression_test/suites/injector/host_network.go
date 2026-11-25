package injector

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

const hostNetworkName = "host-net"

// hostNetworkPort is a container port that no other workload on the node uses.
const hostNetworkPort = 18765

// hostNetworkManifest is a host-network Deployment behind a Service with a
// numeric targetPort, which is what makes the injector add the init container.
const hostNetworkManifest = `apiVersion: v1
kind: Service
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  selector:
    app: %[1]s
  ports:
    - port: 80
      targetPort: %[3]d
---
apiVersion: apps/v1
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
      hostNetwork: true
      dnsPolicy: ClusterFirstWithHostNet
      containers:
        - name: echo
          image: ghcr.io/telepresenceio/echo-server:0.3.1
          imagePullPolicy: IfNotPresent
          env:
            - name: PORT
              value: "%[3]d"
          ports:
            - containerPort: %[3]d
`

// HostNetwork proves that a workload on the host network is not given the
// init container, which would program packet redirection on the node.
type HostNetwork struct {
	rt.Suite
}

func init() {
	rt.Register(&HostNetwork{}, rt.InArea("injector"), rt.NeedsManager(managers.Default))
}

// Test_InterceptRefused intercepts a host-network workload whose Service has
// a numeric targetPort. The intercept must fail with an explanation, and the
// workload's pod must carry neither the init container nor the traffic-agent.
func (s *HostNetwork) Test_InterceptRefused() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	s.Manager()
	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateNamespace(env, "host-net")
	// ns is brand new: the already-running manager only manages it once its
	// pod restarts and re-lists namespaces.
	s.Require().NoError(rt.RestartManager(env))

	path := filepath.Join(r.ArtifactDir("host-net"), "host-net.yaml")
	writeFile(t, path, fmt.Sprintf(hostNetworkManifest, hostNetworkName, ns, hostNetworkPort))
	_, err := r.Kubectl(ctx, ns, "apply", "-f", path)
	s.Require().NoError(err)
	t.Cleanup(func() {
		_, _ = r.Kubectl(ctx, ns, "delete", "deployment,service", hostNetworkName, "--ignore-not-found", "--wait=false")
	})
	_, err = r.Kubectl(ctx, ns, "rollout", "status", "deploy/"+hostNetworkName, "--timeout=120s")
	s.Require().NoError(err)

	rt.Mutate(t, rt.ConnectionFixture(s.AppNamespace())).Disconnect(t)
	rt.Reconnect(t, ctx, ns)

	ls := s.LocalEcho()
	args := []string{"intercept", hostNetworkName, "--namespace", ns}
	for _, o := range []cli.InterceptOpt{rt.ToLocal(ls, "80"), cli.MountFalse()} {
		args = append(args, o()...)
	}
	stdout, stderr, err := s.CLI().Run(ctx, args...)
	s.Require().Error(err, "the intercept should be refused: %s", stdout)
	s.Contains(stdout+stderr, "uses host networking")

	pods, err := podsOf(ctx, r, ns, hostNetworkName)
	s.Require().NoError(err)
	s.Require().NotEmpty(pods, "the workload's pod must exist")
	for i := range pods {
		var names []string
		for _, c := range pods[i].Spec.InitContainers {
			names = append(names, c.Name)
		}
		for _, c := range pods[i].Spec.Containers {
			names = append(names, c.Name)
		}
		s.NotContains(names, "tel-agent-init", "pod %s must not carry the init container", pods[i].Name)
		s.NotContains(names, agentContainerName, "pod %s must not carry a traffic-agent: %s", pods[i].Name, strings.Join(names, ","))
	}
}
