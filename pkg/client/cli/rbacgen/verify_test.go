package rbacgen

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/release"

	auth "k8s.io/api/authorization/v1"
	core "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"

	argov1 "github.com/datawire/argo-rollouts-go-client/pkg/apis/rollouts/v1alpha1"
	argofake "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/authz"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/json"
)

func verifyInstall(t *testing.T, allow func(*auth.ResourceAttributes) bool) *InstallVerdict {
	t.Helper()
	client, _ := sweepClient(testUser, nil, allow)
	v, err := VerifyInstall(context.Background(), &VerifyInstallRequest{ManagerNamespace: "ambassador", Verbs: VerbsInstall, Client: client})
	require.NoError(t, err)
	return v
}

func TestVerifyInstall_NothingMissing(t *testing.T) {
	v := verifyInstall(t, func(*auth.ResourceAttributes) bool { return true })
	assert.True(t, v.OK())
	assert.Equal(t, testUser, v.Identity)
	var sb strings.Builder
	v.WriteText(&sb)
	assert.Contains(t, sb.String(), "alice can install Telepresence")
}

func TestVerifyInstall_Denied(t *testing.T) {
	v := verifyInstall(t, func(ra *auth.ResourceAttributes) bool {
		return !(ra.Verb == "create" && ra.Resource == "mutatingwebhookconfigurations")
	})
	assert.False(t, v.OK())
	require.NotEmpty(t, v.Missing)
	for _, d := range v.Missing {
		assert.Equal(t, "create", d.Verb)
		assert.Equal(t, "mutatingwebhookconfigurations", d.Resource)
		assert.Equal(t, "admissionregistration.k8s.io", d.Group)
	}
	var sb strings.Builder
	v.WriteText(&sb)
	assert.Contains(t, sb.String(), "create mutatingwebhookconfigurations.admissionregistration.k8s.io")
	assert.Contains(t, sb.String(), "rbac generate install")
}

// TestVerifyInstall_RoleRulesNotHeld is the verify side of
// TestGenerateInstall_RoleRulesNotHeld.
func TestVerifyInstall_RoleRulesNotHeld(t *testing.T) {
	eviction := func(ra *auth.ResourceAttributes) bool { return ra.Resource == "pods" && ra.Subresource == "eviction" }

	v := verifyInstall(t, func(ra *auth.ResourceAttributes) bool { return !eviction(ra) && ra.Verb != "escalate" })
	assert.False(t, v.OK())
	assert.Contains(t, v.Missing, DeniedAttribute{Verb: "create", Resource: "pods", Subresource: "eviction"})

	data, err := json.Marshal(v)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"subresource":"eviction"`)

	v = verifyInstall(t, func(ra *auth.ResourceAttributes) bool { return !eviction(ra) })
	assert.True(t, v.OK(), "escalate and bind on the ClusterRole waive its rules")
}

func TestVerifyInstall_Lifecycle(t *testing.T) {
	client, _ := sweepClient(testUser, nil, func(ra *auth.ResourceAttributes) bool { return ra.Resource != "jobs" })
	rq := &VerifyInstallRequest{ManagerNamespace: "ambassador", Verbs: VerbsInstall, Client: client}
	v, err := VerifyInstall(context.Background(), rq)
	require.NoError(t, err)
	assert.True(t, v.OK(), "a first install creates no hook Jobs")

	rq.Verbs = VerbsLifecycle
	v, err = VerifyInstall(context.Background(), rq)
	require.NoError(t, err)
	assert.False(t, v.OK())
	assert.True(t, slices.ContainsFunc(v.Missing, func(d DeniedAttribute) bool { return d.Verb == "create" && d.Resource == "jobs" }))
}

func isTelepresence(ra *auth.ResourceAttributes) bool {
	return ra.Group == "telepresence.io"
}

func isPortForward(ra *auth.ResourceAttributes) bool {
	return ra.Resource == "pods" && ra.Subresource == "portforward"
}

func grantValues(g authz.Grant, external bool) *helm.Values {
	v := &helm.Values{}
	v.Security.Authorization.RequiredGrant = new(string(g))
	v.ExternalEndpoint.Enabled = new(external)
	return v
}

func verifyClient(t *testing.T, rq *VerifyClientRequest, allow func(*auth.ResourceAttributes) bool) *ClientVerdict {
	t.Helper()
	client, _ := sweepClient(testUser, nil, allow)
	rq.Client = client
	if rq.ManagerNamespace == "" {
		rq.ManagerNamespace = "ambassador"
	}
	if rq.Namespaces == nil {
		rq.Namespaces = []string{"dev"}
	}
	v, err := VerifyClient(context.Background(), rq)
	require.NoError(t, err)
	return v
}

func checkOf(t *testing.T, m ClientMode, op, workload string) ClientCheck {
	t.Helper()
	for _, c := range m.Checks {
		if c.Operation == op && c.Workload == workload {
			return c
		}
	}
	require.Failf(t, "no check", "%s %s under %s", op, workload, m.RequiredGrant)
	return ClientCheck{}
}

func requiredPass(m ClientMode) bool {
	for _, c := range m.Checks {
		if c.Required && c.Result != ResultAllowed {
			return false
		}
	}
	return true
}

func TestVerifyClient_GrantModes(t *testing.T) {
	tests := []struct {
		name  string
		allow func(*auth.ResourceAttributes) bool
		grant authz.Grant
		pass  bool
		via   authz.Grant
	}{
		{"telepresence grants under telepresence", isTelepresence, authz.GrantTelepresence, true, authz.GrantTelepresence},
		{"telepresence grants under any", isTelepresence, authz.GrantAny, true, authz.GrantTelepresence},
		{"telepresence grants under portforward", isTelepresence, authz.GrantPortForward, false, ""},
		{"portforward under portforward", isPortForward, authz.GrantPortForward, true, authz.GrantPortForward},
		{"portforward under any", isPortForward, authz.GrantAny, true, authz.GrantPortForward},
		{"portforward under telepresence", isPortForward, authz.GrantTelepresence, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := verifyClient(t, &VerifyClientRequest{Values: grantValues(tt.grant, true)}, tt.allow)
			assert.Nil(t, v.Transport, "an external endpoint needs no port-forward")
			require.Len(t, v.Modes, 1)
			m := v.Modes[0]
			assert.Equal(t, tt.grant, m.RequiredGrant)
			assert.Equal(t, tt.pass, requiredPass(m))
			assert.Equal(t, tt.pass, v.OK())
			for _, op := range []string{OpConnect, OpRead, OpIntercept} {
				c := checkOf(t, m, op, "")
				if tt.pass {
					assert.Equal(t, ResultAllowed, c.Result, op)
					assert.Equal(t, tt.via, c.Grant, op)
					assert.Empty(t, c.Missing, "%s: an allowed check names nothing missing", op)
				} else {
					assert.Equal(t, ResultMissing, c.Result, op)
					assert.NotEmpty(t, c.Missing, op)
				}
			}
		})
	}
}

func TestVerifyClient_AnyNamesBothAlternatives(t *testing.T) {
	v := verifyClient(t, &VerifyClientRequest{Values: grantValues(authz.GrantAny, true)}, func(*auth.ResourceAttributes) bool { return false })
	c := checkOf(t, v.Modes[0], OpConnect, "")
	assert.Equal(t, ResultMissing, c.Result)
	assert.ElementsMatch(t, []DeniedAttribute{
		{Verb: "create", Group: "telepresence.io", Resource: "connections", Namespace: "ambassador"},
		{Verb: "create", Resource: "pods", Subresource: "portforward", Namespace: "ambassador"},
	}, c.Missing)
}

func TestVerifyClient_TransportMiss(t *testing.T) {
	v := verifyClient(t, &VerifyClientRequest{Values: grantValues(authz.GrantTelepresence, false)}, isTelepresence)
	assert.True(t, requiredPass(v.Modes[0]), "the reviews pass")
	require.NotNil(t, v.Transport)
	assert.Equal(t, OpTransport, v.Transport.Operation)
	assert.Equal(t, ResultMissing, v.Transport.Result)
	assert.Equal(t, []DeniedAttribute{{Verb: "create", Resource: "pods", Subresource: "portforward", Namespace: "ambassador"}}, v.Transport.Missing)
	assert.False(t, v.OK())

	// pods/portforward on the manager pod alone is enough.
	v = verifyClient(t, &VerifyClientRequest{Values: grantValues(authz.GrantTelepresence, false)}, func(ra *auth.ResourceAttributes) bool {
		return isTelepresence(ra) || (isPortForward(ra) && ra.Namespace == "ambassador" && ra.Name == "traffic-manager-0")
	})
	assert.Equal(t, ResultAllowed, v.Transport.Result)
	assert.True(t, v.OK())
}

func TestVerifyClient_WorkloadScopedGrant(t *testing.T) {
	allow := func(ra *auth.ResourceAttributes) bool {
		switch ra.Resource {
		case "connections":
			return true
		case "attachments":
			return ra.Verb == "get" && ra.Name == "web"
		}
		return false
	}
	values := grantValues(authz.GrantTelepresence, true)

	v := verifyClient(t, &VerifyClientRequest{Values: values, Workloads: []string{"web"}}, allow)
	m := v.Modes[0]
	assert.Equal(t, ResultAllowed, checkOf(t, m, OpIngest, "web").Result)
	assert.Equal(t, ResultMissing, checkOf(t, m, OpIntercept, "web").Result, "get doesn't authorize an intercept")
	assert.True(t, v.OK())
	for _, n := range v.Notes {
		assert.NotContains(t, n, "--workload")
	}

	v = verifyClient(t, &VerifyClientRequest{Values: values}, allow)
	assert.Equal(t, ResultMissing, checkOf(t, v.Modes[0], OpRead, "").Result)
	assert.False(t, v.OK())
	assert.Contains(t, strings.Join(v.Notes, "\n"), "can't be seen without --workload")
}

func TestVerifyClient_PodsNotListable(t *testing.T) {
	// pods/portforward is granted on single pods only, and the client can't
	// find out which pods the workload has.
	allow := func(ra *auth.ResourceAttributes) bool {
		return isPortForward(ra) && ra.Name != ""
	}
	rq := &VerifyClientRequest{
		Values:    grantValues(authz.GrantPortForward, false),
		Workloads: []string{"web"},
		WorkloadPods: func(context.Context, string, string) ([]string, error) {
			return nil, k8serrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("no"))
		},
	}
	v := verifyClient(t, rq, allow)
	m := v.Modes[0]
	assert.Equal(t, ResultAllowed, checkOf(t, m, OpConnect, "").Result, "connect is grounded on the manager pod")
	c := checkOf(t, m, OpIngest, "web")
	assert.Equal(t, ResultUnknown, c.Result)
	assert.Empty(t, c.Missing)
	assert.False(t, v.OK(), "a required check that could not be determined is not a pass")

	// With the pods known, the pod-scoped grant passes.
	rq.WorkloadPods = func(context.Context, string, string) ([]string, error) { return []string{"web-1"}, nil }
	v = verifyClient(t, rq, allow)
	assert.Equal(t, ResultAllowed, checkOf(t, v.Modes[0], OpIngest, "web").Result)
}

func TestVerifyClient_ReleaseNotReadable(t *testing.T) {
	var lookedUp string
	rq := &VerifyClientRequest{
		ReleaseLookup: func(_ context.Context, ns string) (*release.Release, error) {
			lookedUp = ns
			return nil, k8serrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("no"))
		},
	}
	v := verifyClient(t, rq, isTelepresence)
	assert.Equal(t, "ambassador", lookedUp)
	require.Len(t, v.Modes, 3)
	pass := map[authz.Grant]bool{}
	for _, m := range v.Modes {
		pass[m.RequiredGrant] = requiredPass(m)
	}
	assert.Equal(t, map[authz.Grant]bool{authz.GrantPortForward: false, authz.GrantTelepresence: true, authz.GrantAny: true}, pass)
	require.NotNil(t, v.Transport, "the external endpoint is unknown")
	assert.Contains(t, v.Transport.Reason, "external endpoint")
	assert.Contains(t, strings.Join(v.Notes, "\n"), "--input")
	assert.Equal(t, "unknown", v.Source)

	// The telepresence grant is enough, the port-forward isn't needed by it, and a
	// missing port-forward is only reported since the endpoint may be external.
	assert.False(t, v.Transport.Required)
	assert.Equal(t, ResultMissing, v.Transport.Result)
	assert.Equal(t, []authz.Grant{authz.GrantTelepresence, authz.GrantAny}, v.SatisfiedGrants())
	assert.True(t, v.OK())

	var sb strings.Builder
	v.WriteText(&sb)
	assert.Contains(t, sb.String(), `"telepresence" or "any"`)

	// Nothing granted fails under every mode.
	v = verifyClient(t, &VerifyClientRequest{ReleaseLookup: rq.ReleaseLookup}, func(*auth.ResourceAttributes) bool { return false })
	assert.Empty(t, v.SatisfiedGrants())
	assert.False(t, v.OK())
}

func TestWorkloadPodNames_NotFound(t *testing.T) {
	_, err := WorkloadPodNames(fake.NewClientset(), nil)(context.Background(), "dev", "nope")
	require.ErrorIs(t, err, ErrWorkloadNotFound)

	client, _ := sweepClient(testUser, nil, isTelepresence)
	_, err = VerifyClient(context.Background(), &VerifyClientRequest{
		Values:           grantValues(authz.GrantTelepresence, false),
		Workloads:        []string{"nope"},
		WorkloadPods:     WorkloadPodNames(fake.NewClientset(), nil),
		Client:           client,
		ManagerNamespace: "ambassador",
		Namespaces:       []string{"dev"},
	})
	assert.ErrorIs(t, err, ErrWorkloadNotFound)
}

func TestWorkloadPodNames_Rollout(t *testing.T) {
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "web-1", Namespace: "dev", Labels: map[string]string{"app": "web"}}}
	rollout := &argov1.Rollout{
		ObjectMeta: meta.ObjectMeta{Name: "web", Namespace: "dev"},
		Spec:       argov1.RolloutSpec{Selector: &meta.LabelSelector{MatchLabels: map[string]string{"app": "web"}}},
	}
	pods, err := WorkloadPodNames(fake.NewClientset(pod), argofake.NewSimpleClientset(rollout))(context.Background(), "dev", "web")
	require.NoError(t, err)
	assert.Equal(t, []string{"web-1"}, pods)

	_, err = WorkloadPodNames(fake.NewClientset(pod), argofake.NewSimpleClientset())(context.Background(), "dev", "web")
	assert.ErrorIs(t, err, ErrWorkloadNotFound)
}

func TestVerifyClient_ReleaseValues(t *testing.T) {
	rq := &VerifyClientRequest{
		ReleaseLookup: func(context.Context, string) (*release.Release, error) {
			return &release.Release{Config: map[string]any{
				"security":         map[string]any{"authorization": map[string]any{"requiredGrant": "telepresence"}},
				"externalEndpoint": map[string]any{"enabled": true},
			}}, nil
		},
	}
	v := verifyClient(t, rq, isTelepresence)
	require.Len(t, v.Modes, 1)
	assert.Equal(t, authz.GrantTelepresence, v.Modes[0].RequiredGrant)
	assert.Nil(t, v.Transport)
	assert.True(t, v.OK())
	assert.Contains(t, v.Source, "release")
}

func TestVerifyClient_Diagnostics(t *testing.T) {
	v := verifyClient(t, &VerifyClientRequest{Values: grantValues(authz.GrantAny, true), Namespaces: []string{"a", "b"}},
		func(ra *auth.ResourceAttributes) bool { return isTelepresence(ra) && ra.Subresource == "" })
	require.Len(t, v.Diagnostics, 4)
	for _, c := range v.Diagnostics {
		assert.False(t, c.Required)
		if c.Operation == OpLogs {
			assert.Equal(t, ResultAllowed, c.Result)
		} else {
			assert.Equal(t, ResultMissing, c.Result)
		}
	}
	assert.True(t, v.OK(), "the logs grants are optional")
}

func TestVerifyClient_JSON(t *testing.T) {
	v := verifyClient(t, &VerifyClientRequest{Values: grantValues(authz.GrantPortForward, false)}, func(*auth.ResourceAttributes) bool { return false })
	data, err := json.Marshal(v)
	require.NoError(t, err)
	var back ClientVerdict
	require.NoError(t, json.Unmarshal(data, &back, true))
	assert.Equal(t, *v, back)
	assert.Contains(t, string(data), `"subresource":"portforward"`)
	assert.Contains(t, string(data), `"result":"missing"`)

	var sb strings.Builder
	v.WriteText(&sb)
	assert.Contains(t, sb.String(), "needs create pods/portforward in namespace ambassador")
	assert.Contains(t, sb.String(), "Some required permissions are missing")
}
