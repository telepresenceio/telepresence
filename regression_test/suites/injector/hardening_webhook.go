package injector

import (
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

const injhardCallerName = "injhard-webhook-caller"

// injhardWebhook calls the webhook port of the traffic-manager as someone who
// may port-forward to the manager pod but holds no credential of the
// pre-delete hook.
type injhardWebhook struct {
	rt.Suite
}

func init() {
	rt.Register(&injhardWebhook{}, rt.InArea("injector"), rt.NeedsManager(managers.Default))
}

// caller creates the ServiceAccount, forwards to the manager pod's webhook
// port as it, and returns the local port and the account's token.
func (s *injhardWebhook) caller() (port int, token string) {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	mgrNS := managers.ManagerNamespace
	s.Require().NoError(injhardApply(ctx, r, mgrNS, injhardCallerName, injhardCallerManifest(mgrNS, injhardCallerName)))
	t.Cleanup(func() {
		_, _ = r.Kubectl(ctx, mgrNS, "delete", "serviceaccount,role,rolebinding", injhardCallerName, "--ignore-not-found")
	})
	out, err := r.Kubectl(ctx, mgrNS, "create", "token", injhardCallerName)
	s.Require().NoError(err)
	pod, err := injhardManagerPod(ctx, r)
	s.Require().NoError(err)
	return injhardForward(t, ctx, r, injhardCallerName, pod, 8443), out
}

func injhardAgentedTemplate(name string) workloads.Template {
	tpl := workloads.Echo(name)
	tpl.Annotations = map[string]string{annotation.InjectTrafficAgent: "enabled"}
	return tpl
}

// Test_UninstallRefusedWithoutHookToken sends /uninstall without a token, with
// a malformed one, and with a valid token of another ServiceAccount: each is
// answered 401, pods with an agent keep their UIDs, and a workload created
// afterwards still gets its agent.
func (s *injhardWebhook) Test_UninstallRefusedWithoutHookToken() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	rt.Mutate(t, rt.ManagerFixture(managers.Default))
	injhardRestoreManagerOnFailure(t, rt.Env{Ctx: ctx, T: t, R: r})

	existing := s.Workload(injhardAgentedTemplate("injhard-uninstall-existing"))
	s.Require().Eventually(func() bool {
		uids, err := injhardAgentPodUIDs(ctx, r, ns, existing.Name)
		return err == nil && len(uids) > 0
	}, injhardWait, injhardPoll, "%s should get an agent", existing.Name)
	before, err := injhardAgentPodUIDs(ctx, r, ns, existing.Name)
	s.Require().NoError(err)

	port, token := s.caller()
	for name, header := range map[string]map[string]string{
		"no credential": nil,
		"malformed":     {"Authorization": "Bearer not-a-token"},
		"other account": {"Authorization": "Bearer " + token},
	} {
		code, body, err := injhardWebhookDo(ctx, port, http.MethodDelete, "/uninstall", header, nil)
		s.Require().NoError(err, name)
		s.Require().Equal(http.StatusUnauthorized, code, "%s: %s", name, body)
	}

	s.Never(func() bool {
		uids, err := injhardAgentPodUIDs(ctx, r, ns, existing.Name)
		return err == nil && !slices.Equal(uids, before)
	}, 20*time.Second, injhardPoll, "the agent pods of %s should stay", existing.Name)

	fresh := s.Workload(injhardAgentedTemplate("injhard-uninstall-fresh"))
	s.Require().Eventually(func() bool {
		uids, err := injhardAgentPodUIDs(ctx, r, ns, fresh.Name)
		return err == nil && len(uids) > 0
	}, injhardWait, injhardPoll, "%s should still get an agent", fresh.Name)
}

// Test_ForgedAdmissionLeavesNoConfig sends an admission request for a pod of a
// workload that never asked for an agent, with the inject annotation set on
// the forged pod. The workload's pods created afterwards carry no agent.
func (s *injhardWebhook) Test_ForgedAdmissionLeavesNoConfig() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	rt.Mutate(t, rt.ManagerFixture(managers.Default))

	wl := s.Workload(workloads.Echo("injhard-forged-victim"))
	var pl struct {
		Items []map[string]any `json:"items"`
	}
	s.Require().NoError(r.KubectlJSON(ctx, ns, &pl, "get", "pod", "-l", "app="+wl.Name))
	s.Require().NotEmpty(pl.Items)
	pod := pl.Items[0]
	delete(pod, "status")
	md := pod["metadata"].(map[string]any)
	for _, k := range []string{"resourceVersion", "creationTimestamp", "managedFields"} {
		delete(md, k)
	}
	md["name"] = "injhard-forged-pod"
	md["uid"] = "00000000-0000-4000-8000-0000000000f0"
	anns, _ := md["annotations"].(map[string]any)
	if anns == nil {
		anns = map[string]any{}
	}
	anns[annotation.InjectTrafficAgent] = "enabled"
	md["annotations"] = anns
	review := map[string]any{
		"apiVersion": "admission.k8s.io/v1",
		"kind":       "AdmissionReview",
		"request": map[string]any{
			"uid":       "00000000-0000-4000-8000-0000000000f1",
			"kind":      map[string]any{"group": "", "version": "v1", "kind": "Pod"},
			"resource":  map[string]any{"group": "", "version": "v1", "resource": "pods"},
			"namespace": ns,
			"operation": "CREATE",
			"object":    pod,
		},
	}

	port, _ := s.caller()
	code, body, err := injhardWebhookDo(ctx, port, http.MethodPost, "/traffic-agent",
		map[string]string{"Content-Type": "application/json"}, injhardJSON(t, review))
	s.Require().NoError(err)
	s.Require().Equal(http.StatusOK, code, "%s", body)
	var decision struct {
		Response struct {
			Allowed bool   `json:"allowed"`
			Patch   []byte `json:"patch"`
		} `json:"response"`
	}
	s.Require().NoError(json.Unmarshal(body, &decision), "%s", body)
	s.Require().True(decision.Response.Allowed, "the forged request should be admitted: %s", body)
	s.Require().Contains(string(decision.Response.Patch), agentContainerName, "the review should be answered with an agent patch")

	_, err = r.Kubectl(ctx, ns, "rollout", "restart", "deployment/"+wl.Name)
	s.Require().NoError(err)
	_, err = r.Kubectl(ctx, ns, "rollout", "status", "deployment/"+wl.Name, "--timeout=90s")
	s.Require().NoError(err)
	pods, err := injhardPodsOf(ctx, r, ns, wl.Name)
	s.Require().NoError(err)
	s.Require().NotEmpty(pods, "the restarted deployment should have pods")
	uids, err := injhardAgentPodUIDs(ctx, r, ns, wl.Name)
	s.Require().NoError(err)
	s.Empty(uids, "the restarted pods of %s should have no agent", wl.Name)
}
