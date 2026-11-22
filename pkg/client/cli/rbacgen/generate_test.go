package rbacgen

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authn "k8s.io/api/authentication/v1"
	auth "k8s.io/api/authorization/v1"
	rbac "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files") //nolint:gochecknoglobals // test flag

const testUser = "alice"

// sweepClient answers SelfSubjectReviews with user, or refuses them when
// user is empty, and SelfSubjectAccessReviews with allow. It records every
// access review.
func sweepClient(user string, groups []string, allow func(*auth.ResourceAttributes) bool) (*fake.Clientset, func() []auth.ResourceAttributes) {
	var mu sync.Mutex
	var sent []auth.ResourceAttributes
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(ra *auth.ResourceAttributes) bool {
		mu.Lock()
		sent = append(sent, *ra)
		mu.Unlock()
		return allow(ra)
	})
	client.PrependReactor("create", "selfsubjectreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		review := &authn.SelfSubjectReview{}
		review.Status.UserInfo = authn.UserInfo{Username: user, Groups: groups}
		return true, review, nil
	})
	return client, func() []auth.ResourceAttributes {
		mu.Lock()
		defer mu.Unlock()
		return sent
	}
}

func generate(t *testing.T, rq *InstallRequest) *InstallOutput {
	t.Helper()
	if rq.ManagerNamespace == "" {
		rq.ManagerNamespace = "ambassador"
	}
	if rq.Verbs == "" {
		rq.Verbs = VerbsInstall
	}
	out, err := GenerateInstall(context.Background(), rq)
	require.NoError(t, err)
	return out
}

// documents decodes the generated manifests, keyed by "Kind namespace/name".
func documents(t *testing.T, out *InstallOutput) map[string]map[string]any {
	t.Helper()
	docs := make(map[string]map[string]any)
	for doc := range strings.SplitSeq(string(out.Manifests), "---\n") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, yaml.Unmarshal([]byte(doc), &m))
		md := m["metadata"].(map[string]any)
		ns, _ := md["namespace"].(string)
		docs[m["kind"].(string)+" "+ns+"/"+md["name"].(string)] = m
	}
	return docs
}

func rulesOf(t *testing.T, doc map[string]any) []rbac.PolicyRule {
	t.Helper()
	require.NotNil(t, doc)
	var role rbac.ClusterRole
	require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(doc, &role))
	return role.Rules
}

func hasRule(rules []rbac.PolicyRule, group, resource, verb string, names ...string) bool {
	for _, r := range rules {
		if r.APIGroups[0] == group && r.Resources[0] == resource && slices.Contains(r.Verbs, verb) &&
			strings.Join(r.ResourceNames, ",") == strings.Join(names, ",") {
			return true
		}
	}
	return false
}

func TestGenerateInstall_NothingDenied(t *testing.T) {
	client, _ := sweepClient(testUser, nil, func(*auth.ResourceAttributes) bool { return true })
	out := generate(t, &InstallRequest{Client: client})
	assert.Empty(t, out.Manifests)
	assert.Contains(t, strings.Join(out.Header, "\n"), "alice can already install Telepresence")
}

func TestGenerateInstall_DeniedWebhookAndRole(t *testing.T) {
	client, sent := sweepClient(testUser, nil, func(ra *auth.ResourceAttributes) bool {
		return !(ra.Verb == "create" && (ra.Resource == "mutatingwebhookconfigurations" || ra.Resource == "roles"))
	})
	out := generate(t, &InstallRequest{Client: client})
	docs := documents(t, out)
	name := InstallName("ambassador")
	require.Len(t, docs, 4)

	cr := rulesOf(t, docs["ClusterRole /"+name])
	assert.Equal(t, []rbac.PolicyRule{{
		APIGroups: []string{"admissionregistration.k8s.io"}, Resources: []string{"mutatingwebhookconfigurations"}, Verbs: []string{"create"},
	}}, cr)
	role := rulesOf(t, docs["Role ambassador/"+name])
	assert.Equal(t, []rbac.PolicyRule{{
		APIGroups: []string{"rbac.authorization.k8s.io"}, Resources: []string{"roles"}, Verbs: []string{"create"},
	}}, role)
	for _, r := range append(cr, role...) {
		if slices.Contains(r.Verbs, "create") {
			assert.Empty(t, r.ResourceNames)
		}
	}

	binding := docs["RoleBinding ambassador/"+name]
	assert.Equal(t, []any{map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "User", "name": testUser}}, binding["subjects"])

	for _, ra := range sent() {
		if ra.Verb == "create" {
			assert.Empty(t, ra.Name, "create %s was reviewed with a name", ra.Resource)
		}
	}
}

func TestGenerateInstall_ChartRoleRules(t *testing.T) {
	client, _ := sweepClient(testUser, nil, func(*auth.ResourceAttributes) bool { return false })
	out := generate(t, &InstallRequest{Client: client})
	docs := documents(t, out)
	name := InstallName("ambassador")

	cr := rulesOf(t, docs["ClusterRole /"+name])
	assert.True(t, hasRule(cr, "", "pods/eviction", "create"))
	assert.True(t, hasRule(cr, "", "pods/log", "get"))
	assert.True(t, hasRule(cr, "authentication.k8s.io", "tokenreviews", "create"))
	assert.True(t, hasRule(cr, "", "namespaces", "create"))
	assert.True(t, hasRule(cr, "", "namespaces", "list"), "the lookup lists namespaces")
	assert.True(t, hasRule(cr, "", "configmaps", "get", "traffic-manager"), "the lookup reads every traffic-manager ConfigMap")

	role := rulesOf(t, docs["Role ambassador/"+name])
	assert.True(t, hasRule(role, "", "configmaps", "patch", "traffic-manager", "traffic-manager-install"), "the Role's named rules are copied")
	assert.True(t, hasRule(role, "apps", "statefulsets", "get", "traffic-manager"), "Helm reads the objects it creates")
	assert.True(t, hasRule(role, "apps", "statefulsets", "delete", "traffic-manager"), "a rollback deletes them")
	assert.True(t, hasRule(role, "", "secrets", "list"), "the release storage")
	assert.True(t, hasRule(role, "", "secrets", "delete", "mutator-webhook-tls", releaseSecretName))
	for _, r := range append(cr, role...) {
		assert.False(t, slices.Contains(r.Verbs, "escalate"), "escalate is never granted")
		assert.False(t, slices.Contains(r.Verbs, "bind"), "bind is only granted for a role outside the chart")
	}
}

func TestGenerateInstall_X509Bind(t *testing.T) {
	client, _ := sweepClient(testUser, nil, func(*auth.ResourceAttributes) bool { return false })
	values := &helm.Values{Security: helm.Security{Authentication: helm.Authentication{Mode: new("enforcing")}}}
	out := generate(t, &InstallRequest{Client: client, Values: values})
	docs := documents(t, out)

	role := rulesOf(t, docs["Role kube-system/"+InstallName("ambassador")])
	assert.Contains(t, role, rbac.PolicyRule{
		APIGroups:     []string{"rbac.authorization.k8s.io"},
		Resources:     []string{"roles"},
		ResourceNames: []string{"extension-apiserver-authentication-reader"},
		Verbs:         []string{"bind"},
	})
	assert.True(t, hasRule(role, "rbac.authorization.k8s.io", "rolebindings", "create"))
}

func TestGenerateInstall_ClusterIssuer(t *testing.T) {
	client, _ := sweepClient(testUser, nil, func(*auth.ResourceAttributes) bool { return false })
	values := &helm.Values{AgentInjector: helm.AgentInjector{Certificate: helm.AgentInjectorCertificate{
		Method:      new("certmanager"),
		Certmanager: helm.AgentInjectorCertManager{IssuerRef: helm.IssuerRef{Kind: new("ClusterIssuer")}},
	}}}
	out := generate(t, &InstallRequest{Client: client, Values: values})
	docs := documents(t, out)
	name := InstallName("ambassador")

	assert.True(t, hasRule(rulesOf(t, docs["ClusterRole /"+name]), "cert-manager.io", "clusterissuers", "create"))
	for _, r := range rulesOf(t, docs["Role ambassador/"+name]) {
		assert.NotContains(t, r.Resources, "clusterissuers", "a ClusterIssuer is cluster-scoped")
	}
}

// TestGenerateInstall_RoleRulesNotHeld models an identity that may create
// everything but doesn't hold what the chart's ClusterRole grants.
func TestGenerateInstall_RoleRulesNotHeld(t *testing.T) {
	deniedRule := func(ra *auth.ResourceAttributes) bool {
		return ra.Resource == "pods" && ra.Subresource == "eviction"
	}
	client, _ := sweepClient(testUser, nil, func(ra *auth.ResourceAttributes) bool { return !deniedRule(ra) && ra.Verb != "escalate" })
	out := generate(t, &InstallRequest{Client: client})
	cr := rulesOf(t, documents(t, out)["ClusterRole /"+InstallName("ambassador")])
	assert.Equal(t, []rbac.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods/eviction"}, Verbs: []string{"create"}}}, cr)

	// The same set with the sweep that verify runs.
	render, err := RenderChart(context.Background(), "ambassador", &helm.Values{})
	require.NoError(t, err)
	set, err := HelmInstallSet(render, "ambassador", effectiveValues(t, nil), VerbsInstall)
	require.NoError(t, err)
	denied, err := set.Sweep(context.Background(), client)
	require.NoError(t, err)
	assert.NotEmpty(t, denied)

	// escalate and bind on the ClusterRole waive its rules.
	client, _ = sweepClient(testUser, nil, func(ra *auth.ResourceAttributes) bool { return !deniedRule(ra) })
	denied, err = set.Sweep(context.Background(), client)
	require.NoError(t, err)
	assert.Empty(t, denied)
	out = generate(t, &InstallRequest{Client: client})
	assert.Empty(t, out.Manifests)
}

func effectiveValues(t *testing.T, v *helm.Values) *helm.Values {
	t.Helper()
	if v == nil {
		v = &helm.Values{}
	}
	dv, err := helm.DefaultValues()
	require.NoError(t, err)
	return helm.MergeValues(v, dv)
}

func TestGenerateInstall_Lifecycle(t *testing.T) {
	deny := func(*auth.ResourceAttributes) bool { return false }
	client, sent := sweepClient(testUser, nil, deny)
	out := generate(t, &InstallRequest{Client: client})
	role := rulesOf(t, documents(t, out)["Role ambassador/"+InstallName("ambassador")])
	assert.False(t, hasRule(role, "batch", "jobs", "create"), "a first install creates no hooks")
	assert.False(t, hasRule(role, "apps", "statefulsets", "patch", "traffic-manager"))
	for _, ra := range sent() {
		assert.NotEqual(t, "jobs", ra.Resource)
	}

	client, sent = sweepClient(testUser, nil, deny)
	out = generate(t, &InstallRequest{Client: client, Verbs: VerbsLifecycle})
	role = rulesOf(t, documents(t, out)["Role ambassador/"+InstallName("ambassador")])
	assert.True(t, hasRule(role, "batch", "jobs", "create"))
	assert.True(t, hasRule(role, "batch", "jobs", "watch", "traffic-manager-migrate-statefulset", "uninstall-agents"))
	assert.True(t, hasRule(role, "apps", "statefulsets", "patch", "traffic-manager"))
	assert.True(t, hasRule(role, "", "secrets", "delete"))
	assert.True(t, hasRule(role, "", "serviceaccounts", "create"))
	assert.False(t, hasRule(role, "", "pods", "create"), "the test hook is left out")
	var sawJobs bool
	for _, ra := range sent() {
		sawJobs = sawJobs || ra.Resource == "jobs"
	}
	assert.True(t, sawJobs, "the sweep checks the hook objects")
}

func TestGenerateInstall_Subjects(t *testing.T) {
	allow := func(*auth.ResourceAttributes) bool { return true }
	ctx := context.Background()
	sa := Subject{Kind: rbac.ServiceAccountKind, Namespace: "dev", Name: "installer"}

	t.Run("mismatch", func(t *testing.T) {
		client, _ := sweepClient(testUser, nil, allow)
		_, err := GenerateInstall(ctx, &InstallRequest{ManagerNamespace: "ambassador", Verbs: VerbsInstall, Client: client, Subjects: []Subject{sa}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--as system:serviceaccount:dev:installer")
		assert.Contains(t, err.Error(), "--all")
	})

	t.Run("service account", func(t *testing.T) {
		client, _ := sweepClient("system:serviceaccount:dev:installer", nil, func(ra *auth.ResourceAttributes) bool { return ra.Resource != "roles" })
		out := generate(t, &InstallRequest{Client: client, Subjects: []Subject{sa}, Impersonated: true})
		binding := documents(t, out)["RoleBinding ambassador/"+InstallName("ambassador")]
		assert.Equal(t, []any{map[string]any{"kind": "ServiceAccount", "name": "installer", "namespace": "dev"}}, binding["subjects"])
		assert.Contains(t, strings.Join(out.Header, "\n"), "--as-group")
	})

	t.Run("service account from identity", func(t *testing.T) {
		client, _ := sweepClient("system:serviceaccount:dev:installer", nil, func(ra *auth.ResourceAttributes) bool { return ra.Resource != "roles" })
		out := generate(t, &InstallRequest{Client: client})
		binding := documents(t, out)["RoleBinding ambassador/"+InstallName("ambassador")]
		assert.Equal(t, []any{map[string]any{"kind": "ServiceAccount", "name": "installer", "namespace": "dev"}}, binding["subjects"])
	})

	t.Run("no SelfSubjectReview", func(t *testing.T) {
		client, _ := sweepClient("", nil, allow)
		_, err := GenerateInstall(ctx, &InstallRequest{ManagerNamespace: "ambassador", Verbs: VerbsInstall, Client: client})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--subject")

		out := generate(t, &InstallRequest{Client: client, Subjects: []Subject{sa}})
		assert.Contains(t, strings.Join(out.Header, "\n"), "couldn't confirm")
	})

	t.Run("group", func(t *testing.T) {
		client, _ := sweepClient(testUser, []string{"devs"}, allow)
		out := generate(t, &InstallRequest{Client: client, Subjects: []Subject{{Kind: rbac.GroupKind, Name: "devs"}}})
		assert.Contains(t, strings.Join(out.Header, "\n"), "is a group")
	})

	t.Run("several without --all", func(t *testing.T) {
		client, _ := sweepClient(testUser, nil, allow)
		_, err := GenerateInstall(ctx, &InstallRequest{
			ManagerNamespace: "ambassador", Verbs: VerbsInstall, Client: client,
			Subjects: []Subject{{Kind: rbac.UserKind, Name: testUser}, {Kind: rbac.UserKind, Name: "bob"}},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--all")
	})

	t.Run("all without subject", func(t *testing.T) {
		_, err := GenerateInstall(ctx, &InstallRequest{ManagerNamespace: "ambassador", Verbs: VerbsInstall, All: true})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--subject")
	})
}

func TestGenerateInstall_AllIncludesNamespaceCreate(t *testing.T) {
	out := generate(t, &InstallRequest{All: true, Subjects: []Subject{{Kind: rbac.UserKind, Name: testUser}, {Kind: rbac.GroupKind, Name: "devs"}}})
	docs := documents(t, out)
	cr := rulesOf(t, docs["ClusterRole /"+InstallName("ambassador")])
	assert.True(t, hasRule(cr, "", "namespaces", "create"))
	subjects := docs["ClusterRoleBinding /"+InstallName("ambassador")]["subjects"].([]any)
	assert.Len(t, subjects, 2)
}

func TestGenerateInstall_Golden(t *testing.T) {
	out := generate(t, &InstallRequest{All: true, Subjects: []Subject{{Kind: rbac.UserKind, Name: testUser}}})
	var buf bytes.Buffer
	_, err := out.WriteTo(&buf)
	require.NoError(t, err)
	got := buf.String()
	if version.Version != "" {
		got = strings.ReplaceAll(got, version.Version, "VERSION")
	}

	path := filepath.Join("testdata", "generate_install_default.golden")
	if *updateGolden {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), got)
}
