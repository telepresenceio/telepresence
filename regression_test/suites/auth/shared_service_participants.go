package auth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

const (
	sharedPartService = "auth-shared-part"
	sharedPartGroup   = "auth-shared-part-group"
	sharedPartImage   = "ghcr.io/telepresenceio/echo-server:0.3.1"
	sharedPartPort    = 8080
	sharedPartQuiet   = 20 * time.Second
	sharedPartPoll    = 2 * time.Second
)

// sharedPartManifest holds one Service selecting the pods of two
// single-replica Deployments. Arguments: namespace, group, service, image,
// port, then the two Deployment names.
const sharedPartManifest = `apiVersion: v1
kind: Service
metadata:
  name: %[3]s
  namespace: %[1]s
spec:
  selector:
    group: %[2]s
  ports:
    - name: http
      port: %[5]d
      targetPort: http
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[6]s
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %[6]s
  template:
    metadata:
      labels:
        app: %[6]s
        group: %[2]s
    spec:
      containers:
        - name: echo
          image: %[4]s
          imagePullPolicy: IfNotPresent
          ports:
            - name: http
              containerPort: %[5]d
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[7]s
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %[7]s
  template:
    metadata:
      labels:
        app: %[7]s
        group: %[2]s
    spec:
      containers:
        - name: echo
          image: %[4]s
          imagePullPolicy: IfNotPresent
          ports:
            - name: http
              containerPort: %[5]d
`

// Test_SharedServiceInterceptDoesNotJoinUngrantedWorkload makes the manager
// calls of a client that holds the attachment grant for workload A only and
// intercepts A through a Service that also selects workload B, leaving
// service_name empty. After B's pod is replaced and its traffic-agent
// registers again, the intercept the manager reports must not list B.
func (s *AuthEnforcing) Test_SharedServiceInterceptDoesNotJoinUngrantedWorkload() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()
	nameA, nameB := "auth-shared-part-a", "auth-shared-part-b"

	path := filepath.Join(t.TempDir(), "shared.yaml")
	manifest := fmt.Sprintf(sharedPartManifest,
		ns, sharedPartGroup, sharedPartService, sharedPartImage, sharedPartPort, nameA, nameB)
	s.Require().NoError(os.WriteFile(path, []byte(manifest), 0o644))
	_, err := r.Kubectl(ctx, ns, "apply", "-f", path)
	s.Require().NoError(err, "applying the shared-service workloads")
	t.Cleanup(func() {
		for _, args := range [][]string{
			{"delete", "deployment", nameA, nameB, "--ignore-not-found"},
			{"delete", "service", sharedPartService, "--ignore-not-found"},
		} {
			if _, err := r.Kubectl(ctx, ns, args...); err != nil {
				t.Errorf("cleanup %v: %v", args, err)
			}
		}
	})
	for _, name := range []string{nameA, nameB} {
		_, err = r.Kubectl(ctx, ns, "rollout", "status", "deployment/"+name, "--timeout=120s")
		s.Require().NoError(err, "waiting for %s", name)
	}

	wlA := &rt.Workload{Name: nameA, Namespace: ns, Kind: "Deployment", Port: sharedPartPort, SvcName: sharedPartService}
	wlB := &rt.Workload{Name: nameB, Namespace: ns, Kind: "Deployment", Port: sharedPartPort, SvcName: sharedPartService}
	installAgents(s, ns, wlA, wlB)

	const identityName = "rtest-auth-shared-part"
	createGrantIdentity(t, ctx, r, identityName, minimalRBACRules)
	grantInNamespace(t, ctx, r, identityName, ns, workloadAttachmentRules(nameA))
	tok := kubectlCreateToken(t, ctx, r, identityName)
	mc, mctx, si := dialAndArrive(t, ctx, r, ns, identityName, tok)

	spec := &manager.InterceptSpec{
		Name:          "rtest-shared-part",
		Client:        identityName,
		Agent:         nameA,
		Namespace:     ns,
		WorkloadKind:  "Deployment",
		Mechanism:     "http",
		TargetHost:    "127.0.0.1",
		TargetPort:    9,
		HeaderFilters: map[string]string{"x-rtest-shared-part": "1"},
	}
	pi, err := mc.PrepareIntercept(mctx, &manager.CreateInterceptRequest{Session: si, InterceptSpec: spec})
	s.Require().NoError(err, "PrepareIntercept for %s", nameA)
	s.Require().Empty(pi.Error)
	s.Require().NotEmpty(pi.ServiceUid)

	spec.ServiceUid = pi.ServiceUid
	spec.ServicePort = pi.ServicePort
	spec.ServicePortName = pi.ServicePortName
	spec.Protocol = pi.Protocol
	spec.ContainerName = pi.ContainerName
	spec.ContainerPort = pi.ContainerPort
	spec.PodPorts = pi.PodPorts
	spec.ServiceIps = pi.ServiceIps
	spec.WorkloadKind = pi.WorkloadKind
	spec.DialTimeout = int64(5 * time.Second)
	spec.RoundtripLatency = int64(2 * time.Second)

	_, err = mc.CreateIntercept(mctx, &manager.CreateInterceptRequest{Session: si, InterceptSpec: spec})
	s.Require().NoError(err, "CreateIntercept for %s", nameA)
	t.Cleanup(func() {
		_, _ = mc.RemoveIntercept(metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok),
			&manager.RemoveInterceptRequest2{Session: si, Name: spec.Name})
	})

	// Replace B's pod so that its traffic-agent registers with the manager again.
	before := agentPodUIDs(ctx, r, ns, nameB)
	s.Require().NotEmpty(before, "%s should have a traffic-agent", nameB)
	_, err = r.Kubectl(ctx, ns, "rollout", "restart", "deployment/"+nameB)
	s.Require().NoError(err)
	_, err = r.Kubectl(ctx, ns, "rollout", "status", "deployment/"+nameB, "--timeout=120s")
	s.Require().NoError(err, "waiting for %s", nameB)
	s.Require().Eventually(func() bool {
		after := agentPodUIDs(ctx, r, ns, nameB)
		return len(after) > 0 && !slices.ContainsFunc(after, func(uid string) bool { return slices.Contains(before, uid) })
	}, namespaceReadTimeout, namespaceReadPoll, "%s should get a new pod with a traffic-agent", nameB)

	// Each read completes before the test goes on, so none is in flight
	// when the cleanup removes the intercept.
	for deadline := time.Now().Add(sharedPartQuiet); time.Now().Before(deadline); time.Sleep(sharedPartPoll) {
		listed, err := interceptListsWorkload(mctx, mc, si, spec.Name, nameB)
		s.Require().NoError(err, "reading intercept %s", spec.Name)
		s.Require().False(listed, "the intercept must not list %s", nameB)
	}
}

// interceptListsWorkload reports whether the manager's answer for the
// intercept lists a participating workload named name. A failed read is
// returned as an error.
func interceptListsWorkload(ctx context.Context, mc manager.ManagerClient, si *manager.SessionInfo, intercept, name string) (bool, error) {
	ii, err := mc.GetIntercept(ctx, &manager.GetInterceptRequest{Session: si, Name: intercept})
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(ii.ServiceWorkloads, func(w *manager.InterceptWorkload) bool {
		return w.WorkloadName == name
	}), nil
}
