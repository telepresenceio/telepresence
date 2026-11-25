package injector

import (
	"encoding/json/v2"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// injhardMarkerImage is a distinct image name that no registry serves.
const injhardMarkerImage = "registry.invalid/injhard/traffic-agent:marker"

// injhardConfig covers what the traffic-manager derives from objects that
// users of a managed namespace can create.
type injhardConfig struct {
	rt.Suite
}

func init() {
	rt.Register(&injhardConfig{}, rt.InArea("injector"), rt.NeedsManager(managers.Default))
}

// Test_PodAnnotationDoesNotConfigureOtherNamespace puts an agent config
// annotation that names a workload of another namespace on the pods of a
// workload in a private namespace, and restarts the traffic-manager. The
// restarted pods of the named workload keep the containers they had: no
// traffic-agent with the annotation's image.
func (s *injhardConfig) Test_PodAnnotationDoesNotConfigureOtherNamespace() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	rt.Mutate(t, rt.ManagerFixture(managers.Default))
	env := rt.Env{Ctx: ctx, T: t, R: r}
	attackerNS := rt.PrivateNamespace(env, "injhard-a")
	victimNS := rt.PrivateNamespace(env, "injhard-v")
	s.Require().NoError(rt.RestartManager(env))

	const name = "injhard-victim"
	victim := rt.Get(t, rt.WorkloadFixture(victimNS, workloads.Echo(name)))
	t.Cleanup(func() {
		for _, ns := range []string{victimNS, attackerNS} {
			_, _ = r.Kubectl(ctx, ns, "delete", "deployment", "--all", "--wait=false")
		}
	})

	// A donor in the attacker's namespace supplies a config of the right shape.
	donor := rt.Get(t, rt.WorkloadFixture(attackerNS, injhardAgentedTemplate(name)))
	var annotated string
	s.Require().Eventually(func() bool {
		var pl injhardPodList
		if err := r.KubectlJSON(ctx, attackerNS, &pl, "get", "pod", "-l", "app="+donor.Name); err != nil {
			return false
		}
		for _, it := range pl.Items {
			if a := it.Metadata.Annotations[annotation.Config]; a != "" {
				annotated = a
				return true
			}
		}
		return false
	}, injhardWait, injhardPoll, "the donor pod should carry an agent config")

	var cfg map[string]any
	s.Require().NoError(json.Unmarshal([]byte(annotated), &cfg))
	uid, err := r.Kubectl(ctx, victimNS, "get", "service", victim.SvcName, "-o", "jsonpath={.metadata.uid}")
	s.Require().NoError(err)
	cfg["namespace"] = victimNS
	cfg["agentImage"] = injhardMarkerImage
	cfg["securityContext"] = map[string]any{"runAsUser": 7331}
	if cns, ok := cfg["containers"].([]any); ok {
		for _, c := range cns {
			ics, _ := c.(map[string]any)["intercepts"].([]any)
			for _, ic := range ics {
				ic.(map[string]any)["serviceUID"] = strings.TrimSpace(uid)
			}
		}
	}
	forged := string(injhardJSON(t, cfg))

	_, err = r.Kubectl(ctx, attackerNS, "delete", "deployment,service", donor.Name, "--wait")
	s.Require().NoError(err)

	holder := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: injhard-holder
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: injhard-holder
  template:
    metadata:
      labels:
        app: injhard-holder
      annotations:
        %[2]s: %[3]s
    spec:
      containers:
        - name: holder
          image: %[4]s
`, attackerNS, annotation.Config, strconv.Quote(forged), workloads.Echo(name).Image)
	s.Require().NoError(injhardApply(ctx, r, attackerNS, "injhard-holder", holder))
	_, err = r.Kubectl(ctx, attackerNS, "rollout", "status", "deployment/injhard-holder", "--timeout=90s")
	s.Require().NoError(err)

	before, err := injhardPodsOf(ctx, r, victimNS, name)
	s.Require().NoError(err)
	s.Require().NotEmpty(before)
	s.Require().NoError(rt.RestartManager(env))

	_, err = r.Kubectl(ctx, victimNS, "rollout", "restart", "deployment/"+name)
	s.Require().NoError(err)
	_, err = r.Kubectl(ctx, victimNS, "rollout", "status", "deployment/"+name, "--timeout=90s")
	s.Require().NoError(err, "the restarted pods of %s should become ready", name)

	after, err := injhardPodsOf(ctx, r, victimNS, name)
	s.Require().NoError(err)
	s.Require().NotEmpty(after, "the restarted deployment should have pods")
	for _, p := range after {
		s.NotEqual(before[0].UID, p.UID, "pod %s should be a replacement pod", p.UID)
		s.Equal(before[0].Containers, p.Containers, "pod %s should have the containers it had before", p.UID)
		for _, c := range p.Containers {
			s.NotContains(c, "injhard/traffic-agent", "pod %s", p.UID)
		}
	}
}

// Test_UndeclaredPortAnnotationKeepsManagerRunning creates a workload whose
// inject-container-ports annotation names a numeric port that no container
// declares, and restarts the traffic-manager: it becomes Ready and does not
// restart.
func (s *injhardConfig) Test_UndeclaredPortAnnotationKeepsManagerRunning() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	rt.Mutate(t, rt.ManagerFixture(managers.Default))
	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateNamespace(env, "injhard-port")

	tpl := workloads.EchoNoService("injhard-port")
	tpl.Annotations = map[string]string{
		annotation.InjectTrafficAgent:   "enabled",
		annotation.InjectContainerPorts: "9999",
	}
	wl := rt.Get(t, rt.WorkloadFixture(ns, tpl))
	t.Cleanup(func() {
		_, _ = r.Kubectl(ctx, ns, "delete", "deployment", wl.Name, "--ignore-not-found", "--wait=false")
		if t.Failed() {
			if err := rt.RestartManager(env); err != nil {
				r.Infof("[rtest] injector: restarting the traffic-manager: %v", err)
			}
		}
	})

	s.Require().NoError(rt.RestartManager(env), "the traffic-manager should become ready with the workload present")

	managerRestarts := func() (ready bool, restarts []string) {
		out, err := r.Kubectl(ctx, managers.ManagerNamespace, "get", "pod", "-l", "app=traffic-manager", "-o",
			`jsonpath={range .items[*]}{.status.conditions[?(@.type=="Ready")].status}|{.status.containerStatuses[*].restartCount}{"\n"}{end}`)
		if err != nil {
			return false, nil
		}
		ready = true
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			st, rc, _ := strings.Cut(line, "|")
			ready = ready && st == "True"
			restarts = append(restarts, strings.Fields(rc)...)
		}
		return ready, restarts
	}
	s.Never(func() bool {
		ready, restarts := managerRestarts()
		return !ready || slices.ContainsFunc(restarts, func(n string) bool { return n != "0" })
	}, 20*time.Second, injhardPoll, "the traffic-manager should stay ready without restarting")
}

// Test_ServiceCreatorCannotRestartAgentPods creates, as an account that may
// only create Services, a Service that selects a workload with an agent. The
// pods of the workload are not replaced.
func (s *injhardConfig) Test_ServiceCreatorCannotRestartAgentPods() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	rt.Mutate(t, rt.ManagerFixture(managers.Default))

	wl := s.Workload(injhardAgentedTemplate("injhard-svc-victim"))
	s.Require().Eventually(func() bool {
		uids, err := injhardAgentPodUIDs(ctx, r, ns, wl.Name)
		return err == nil && len(uids) > 0
	}, injhardWait, injhardPoll, "%s should get an agent", wl.Name)
	before, err := injhardAgentPodUIDs(ctx, r, ns, wl.Name)
	s.Require().NoError(err)

	const sa = "injhard-svc-creator"
	setup := fmt.Sprintf(`apiVersion: v1
kind: ServiceAccount
metadata:
  name: %[2]s
  namespace: %[1]s
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: %[2]s
  namespace: %[1]s
rules:
  - apiGroups: [""]
    resources: ["services"]
    verbs: ["create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: %[2]s
  namespace: %[1]s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: %[2]s
subjects:
  - kind: ServiceAccount
    name: %[2]s
    namespace: %[1]s
`, ns, sa)
	s.Require().NoError(injhardApply(ctx, r, ns, sa, setup))
	t.Cleanup(func() {
		_, _ = r.Kubectl(ctx, ns, "delete", "service", "injhard-svc-extra", "--ignore-not-found")
		_, _ = r.Kubectl(ctx, ns, "delete", "serviceaccount,role,rolebinding", sa, "--ignore-not-found")
	})

	svc := fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: injhard-svc-extra
  namespace: %s
spec:
  selector:
    app: %s
  ports:
    - name: extra
      port: 8081
      targetPort: http
`, ns, wl.Name)
	path, err := injhardManifestPath(r, "injhard-svc-extra", svc)
	s.Require().NoError(err)
	_, err = r.Kubectl(ctx, ns, "--as", "system:serviceaccount:"+ns+":"+sa, "create", "-f", path)
	s.Require().NoError(err, "the account should be able to create the Service")

	s.Never(func() bool {
		uids, err := injhardAgentPodUIDs(ctx, r, ns, wl.Name)
		return err == nil && !slices.Equal(uids, before)
	}, 45*time.Second, injhardPoll, "the agent pods of %s should stay", wl.Name)
}

// Test_OwnerReferenceCycleKeepsManagerRunning creates two StatefulSets whose
// controller owner references name each other, then starts a pod of one of
// them. The traffic-manager resolves the pod's owner without crashing, does
// not restart, and still serves a connection.
func (s *injhardConfig) Test_OwnerReferenceCycleKeepsManagerRunning() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	rt.Mutate(t, rt.ManagerFixture(managers.Default))
	env := rt.Env{Ctx: ctx, T: t, R: r}
	injhardRestoreManagerOnFailure(t, env)

	const nameA, nameB = "injhard-cycle-a", "injhard-cycle-b"
	manifest := func(name string) string {
		return fmt.Sprintf(`apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: %[2]s
  namespace: %[1]s
spec:
  replicas: 0
  serviceName: %[2]s
  selector:
    matchLabels:
      app: %[2]s
  template:
    metadata:
      labels:
        app: %[2]s
    spec:
      containers:
        - name: echo
          image: ghcr.io/telepresenceio/echo-server:0.3.1
          ports:
            - name: http
              containerPort: 8080
`, ns, name)
	}
	t.Cleanup(func() {
		for _, name := range []string{nameA, nameB} {
			_, _ = r.Kubectl(ctx, ns, "patch", "statefulset", name, "--type=merge", "-p", `{"metadata":{"ownerReferences":null}}`)
		}
		_, _ = r.Kubectl(ctx, ns, "delete", "statefulset", nameA, nameB, "--ignore-not-found", "--wait=false")
	})
	s.Require().NoError(injhardApply(ctx, r, ns, nameA, manifest(nameA)))
	s.Require().NoError(injhardApply(ctx, r, ns, nameB, manifest(nameB)))

	uid := func(name string) string {
		out, err := r.Kubectl(ctx, ns, "get", "statefulset", name, "-o", "jsonpath={.metadata.uid}")
		s.Require().NoError(err)
		return strings.TrimSpace(out)
	}
	ownedBy := func(owner, ownerUID string) string {
		return fmt.Sprintf(`{"metadata":{"ownerReferences":[{"apiVersion":"apps/v1","kind":"StatefulSet","name":%q,"uid":%q,"controller":true}]}}`,
			owner, ownerUID)
	}
	_, err := r.Kubectl(ctx, ns, "patch", "statefulset", nameA, "--type=merge", "-p", ownedBy(nameB, uid(nameB)))
	s.Require().NoError(err)
	_, err = r.Kubectl(ctx, ns, "patch", "statefulset", nameB, "--type=merge", "-p", ownedBy(nameA, uid(nameA)))
	s.Require().NoError(err)

	managerRestarts := func() string {
		out, err := r.Kubectl(ctx, managers.ManagerNamespace, "get", "pod", "-l", "app=traffic-manager", "-o",
			`jsonpath={range .items[*]}{.metadata.uid}|{.status.containerStatuses[*].restartCount}{"\n"}{end}`)
		if err != nil {
			return "error: " + err.Error()
		}
		return strings.TrimSpace(out)
	}
	before := managerRestarts()

	_, err = r.Kubectl(ctx, ns, "scale", "statefulset", nameA, "--replicas=1")
	s.Require().NoError(err)
	s.Require().Eventually(func() bool {
		out, err := r.Kubectl(ctx, ns, "get", "pod", nameA+"-0", "-o", "jsonpath={.metadata.name}")
		return err == nil && strings.TrimSpace(out) != ""
	}, injhardWait, injhardPoll, "the pod of %s should be created", nameA)

	s.Never(func() bool {
		return managerRestarts() != before
	}, 30*time.Second, injhardPoll, "the traffic-manager should keep running with the same restart count")

	s.Connect()
}
