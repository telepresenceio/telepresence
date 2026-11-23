package nodeagent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// NodeAgentOverlap proves that node-agent Jobs target only pods that the
// attached workload controls, even when its selector also matches the pods
// of another workload.
type NodeAgentOverlap struct {
	rt.Suite
}

func init() {
	rt.Register(&NodeAgentOverlap{}, rt.InArea("nodeagent"), rt.NeedsManager(managers.NodeAgent()))
}

const (
	overlapNameA  = "na-overlap-a"
	overlapNameB  = "na-overlap-b"
	overlapMarker = "rtest-overlap"
)

// overlapDeploymentB is a Deployment whose pods carry app=na-overlap-a, so
// that the selector of workload A matches them, plus a label that A's pods
// lack. It has a container named like A's.
const overlapDeploymentB = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %[3]s
      %[4]s: b
  template:
    metadata:
      labels:
        app: %[3]s
        %[4]s: b
    spec:
      containers:
        - name: echo-server
          image: ghcr.io/telepresenceio/echo-server:0.3.1
          ports:
            - containerPort: 8080
          env:
            - name: PORT
              value: "8080"
`

// Test_JobsTargetOnlyOwnedPods attaches a node-agent intercept to workload A
// whose selector also matches the pod of workload B. Every node-agent Job
// must target a pod of A, and none may target the pod of B.
func (s *NodeAgentOverlap) Test_JobsTargetOnlyOwnedPods() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	conn := s.Connect()
	wl := s.Workload(workloads.Echo(overlapNameA))
	ls := s.LocalEcho()

	path := filepath.Join(t.TempDir(), "overlap-b.yaml")
	s.Require().NoError(os.WriteFile(path,
		[]byte(fmt.Sprintf(overlapDeploymentB, overlapNameB, ns, overlapNameA, overlapMarker)), 0o644))
	_, err := r.Kubectl(ctx, ns, "apply", "-f", path)
	s.Require().NoError(err)
	t.Cleanup(func() {
		_, _ = r.Kubectl(ctx, ns, "delete", "deployment", overlapNameB, "--ignore-not-found", "--wait=false")
	})
	_, err = r.Kubectl(ctx, ns, "rollout", "status", "deployment/"+overlapNameB, "--timeout=120s")
	s.Require().NoError(err)

	podNames := func(selector string) []string {
		out, err := r.Kubectl(ctx, ns, "get", "pods", "-l", selector, "-o", "jsonpath={.items[*].metadata.name}")
		s.Require().NoError(err)
		return strings.Fields(out)
	}
	podsA := podNames("app=" + overlapNameA + ",!" + overlapMarker)
	podsB := podNames(overlapMarker + "=b")
	s.Require().Len(podsA, 1)
	s.Require().Len(podsB, 1)

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), nodeAgentFlag())
	defer a.Detach(t)

	targets := func() []string {
		out, err := r.Kubectl(ctx, managers.ManagerNamespace, "get", "jobs",
			"-l", "app=traffic-node-agent,telepresence.io/agentName="+wl.Name+",telepresence.io/workloadNamespace="+wl.Namespace,
			"-o", `jsonpath={range .items[*]}{.metadata.labels.telepresence\.io/targetPod}{"\n"}{end}`)
		if err != nil {
			return nil
		}
		return strings.Fields(out)
	}
	s.Require().Eventually(func() bool { return len(targets()) > 0 }, jobPollTimeout, jobPollInterval,
		"expected a node-agent Job for %s", wl.Name)

	ok := func() bool {
		for _, tp := range targets() {
			if tp != podsA[0] {
				return false
			}
		}
		return true
	}
	s.Require().Never(func() bool { return !ok() }, 10*time.Second, jobPollInterval,
		"node-agent Jobs for %s must target only its own pod %s, not %s", wl.Name, podsA[0], podsB[0])
}
