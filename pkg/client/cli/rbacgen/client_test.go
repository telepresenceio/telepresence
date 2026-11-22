package rbacgen

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"

	core "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

var alice = Subject{Kind: rbac.UserKind, Name: testUser} //nolint:gochecknoglobals // test constant

func generateClient(t *testing.T, rq *ClientRequest) *ClientOutput {
	t.Helper()
	if rq.ManagerNamespace == "" {
		rq.ManagerNamespace = "ambassador"
	}
	if rq.Subjects == nil {
		rq.Subjects = []Subject{alice}
	}
	if rq.Format == "" {
		rq.Format = FormatRBAC
	}
	out, err := GenerateClient(context.Background(), rq)
	require.NoError(t, err)
	return out
}

func clientDocs(t *testing.T, out *ClientOutput) map[string]map[string]any {
	t.Helper()
	return documents(t, &InstallOutput{Manifests: out.Body})
}

func allRules(t *testing.T, docs map[string]map[string]any) []rbac.PolicyRule {
	t.Helper()
	var rules []rbac.PolicyRule
	for key, doc := range docs {
		if strings.HasPrefix(key, "Role ") || strings.HasPrefix(key, "ClusterRole ") {
			rules = append(rules, rulesOf(t, doc)...)
		}
	}
	return rules
}

func docKeys(docs map[string]map[string]any) []string {
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	return keys
}

func TestGenerateClient_ClusterWide(t *testing.T) {
	out := generateClient(t, &ClientRequest{Values: &helm.Values{}})
	docs := clientDocs(t, out)
	name := ClientName("ambassador")
	assert.ElementsMatch(t, []string{
		"ClusterRole /" + name,
		"ClusterRoleBinding /" + name,
		"Role ambassador/" + name + "-connect",
		"RoleBinding ambassador/" + name + "-connect",
	}, docKeys(docs))

	cr := rulesOf(t, docs["ClusterRole /"+name])
	assert.True(t, hasRule(cr, "telepresence.io", "logs", "get"))
	assert.True(t, hasRule(cr, "", "pods/portforward", "create"))

	binding := docs["ClusterRoleBinding /"+name]
	assert.Equal(t, map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": name}, binding["roleRef"])
	assert.Equal(t, []any{map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "User", "name": testUser}}, binding["subjects"])
	connect := docs["RoleBinding ambassador/"+name+"-connect"]
	assert.Equal(t, name+"-connect", connect["roleRef"].(map[string]any)["name"])

	for _, doc := range docs {
		assert.NotContains(t, doc["metadata"], "labels", "the chart's Helm labels are dropped")
	}
	assert.Contains(t, strings.Join(out.Header, "\n"), "cluster-wide")
}

func TestGenerateClient_Namespaced(t *testing.T) {
	values := &helm.Values{Namespaces: []string{"b", "a"}}
	out := generateClient(t, &ClientRequest{Values: values})
	docs := clientDocs(t, out)
	name := ClientName("ambassador")
	assert.ElementsMatch(t, []string{
		"Role a/" + name, "RoleBinding a/" + name,
		"Role b/" + name, "RoleBinding b/" + name,
		"Role ambassador/" + name + "-connect", "RoleBinding ambassador/" + name + "-connect",
		"Role ambassador/" + name + "-logs", "RoleBinding ambassador/" + name + "-logs",
	}, docKeys(docs))
	assert.Contains(t, strings.Join(out.Header, "\n"), "Namespaces: a, b, which is every namespace")

	out = generateClient(t, &ClientRequest{Values: values, Namespaces: []string{"a"}})
	docs = clientDocs(t, out)
	assert.Contains(t, docs, "Role a/"+name)
	assert.NotContains(t, docs, "Role b/"+name)

	_, err := GenerateClient(context.Background(), &ClientRequest{
		ManagerNamespace: "ambassador", Subjects: []Subject{alice}, Values: values, Namespaces: []string{"c"}, Format: FormatRBAC,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only manages a, b")
}

func TestGenerateClient_DynamicSelector(t *testing.T) {
	values := &helm.Values{NamespaceSelector: meta.LabelSelector{MatchLabels: map[string]string{"team": "x"}}}
	client := fake.NewClientset(
		&core.Namespace{ObjectMeta: meta.ObjectMeta{Name: "dev", Labels: map[string]string{"team": "x"}}},
		&core.Namespace{ObjectMeta: meta.ObjectMeta{Name: "other", Labels: map[string]string{"team": "y"}}},
	)
	out := generateClient(t, &ClientRequest{Values: values, Client: client, Namespaces: []string{"dev"}})
	assert.Contains(t, clientDocs(t, out), "Role dev/"+ClientName("ambassador"))

	_, err := GenerateClient(context.Background(), &ClientRequest{
		ManagerNamespace: "ambassador", Subjects: []Subject{alice}, Values: values, Client: client, Namespaces: []string{"other"}, Format: FormatRBAC,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "team=x")

	// A cluster-wide manager never selects kube-system.
	_, err = GenerateClient(context.Background(), &ClientRequest{
		ManagerNamespace: "ambassador", Subjects: []Subject{alice}, Values: &helm.Values{}, Client: client, Namespaces: []string{"kube-system"}, Format: FormatRBAC,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kube-system")

	out = generateClient(t, &ClientRequest{Values: values, Namespaces: []string{"dev"}})
	assert.Contains(t, strings.Join(out.Header, "\n"), "Unable to read the labels of namespace dev")
}

func TestGenerateClient_ExternalEndpoint(t *testing.T) {
	values := &helm.Values{
		ExternalEndpoint: helm.ExternalEndpoint{
			Enabled: new(true),
			TLS:     helm.ExternalTLS{SecretName: new("tm-tls")},
		},
		Security: helm.Security{Authentication: helm.Authentication{Mode: new("enforcing")}},
	}
	docs := clientDocs(t, generateClient(t, &ClientRequest{Values: values}))
	for _, r := range allRules(t, docs) {
		assert.NotContains(t, r.Resources, "pods/portforward")
	}
	assert.True(t, hasRule(allRules(t, docs), "telepresence.io", "connections", "create"))
}

func TestGenerateClient_LegacyAccess(t *testing.T) {
	name := ClientName("ambassador")
	docs := clientDocs(t, generateClient(t, &ClientRequest{Values: &helm.Values{ClientRbac: helm.ClientRbac{LegacyAccess: new(true)}}}))
	cr := rulesOf(t, docs["ClusterRole /"+name])
	assert.True(t, hasRule(cr, "", "pods/log", "get"))
	assert.True(t, hasRule(cr, "", "namespaces", "list"))
	connect := rulesOf(t, docs["Role ambassador/"+name+"-connect"])
	assert.True(t, hasRule(connect, "", "services", "get", "traffic-manager"))

	docs = clientDocs(t, generateClient(t, &ClientRequest{Values: &helm.Values{ClientRbac: helm.ClientRbac{LegacyAccess: new(false)}}}))
	cr = rulesOf(t, docs["ClusterRole /"+name])
	assert.False(t, hasRule(cr, "", "pods/log", "get"))
	assert.False(t, hasRule(cr, "", "namespaces", "list"))
	connect = rulesOf(t, docs["Role ambassador/"+name+"-connect"])
	assert.Equal(t, []rbac.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"pods/portforward"}, ResourceNames: []string{"traffic-manager-0"}, Verbs: []string{"create"}},
		{APIGroups: []string{"telepresence.io"}, Resources: []string{"connections"}, Verbs: []string{"create"}},
	}, connect)
}

func TestGenerateClient_Release(t *testing.T) {
	ctx := context.Background()
	rq := func(lookup func(context.Context, string) (*release.Release, error)) *ClientRequest {
		return &ClientRequest{ManagerNamespace: "ambassador", Subjects: []Subject{alice}, Format: FormatRBAC, ReleaseLookup: lookup}
	}

	t.Run("refused", func(t *testing.T) {
		_, err := GenerateClient(ctx, rq(func(context.Context, string) (*release.Release, error) {
			return nil, k8serrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", errors.New("no"))
		}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Helm release secrets")
		assert.Contains(t, err.Error(), "--input")
	})

	t.Run("not installed", func(t *testing.T) {
		_, err := GenerateClient(ctx, rq(func(context.Context, string) (*release.Release, error) { return nil, nil }))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--input")
	})

	t.Run("installed", func(t *testing.T) {
		out := generateClient(t, rq(func(_ context.Context, ns string) (*release.Release, error) {
			assert.Equal(t, "ambassador", ns)
			return &release.Release{
				Namespace: ns,
				Config:    map[string]any{"namespaces": []any{"dev"}},
				Chart:     &chart.Chart{Metadata: &chart.Metadata{Version: "0.0.1"}},
			}, nil
		}))
		assert.Contains(t, clientDocs(t, out), "Role dev/"+ClientName("ambassador"))
		header := strings.Join(out.Header, "\n")
		assert.Contains(t, header, "release in namespace ambassador")
		assert.Contains(t, header, "chart version 0.0.1")
	})
}

// TestGenerateClient_ValuesRoundTrip renders the chart with the emitted
// values block and expects the objects of the rbac format.
func TestGenerateClient_ValuesRoundTrip(t *testing.T) {
	ctx := context.Background()
	sa := Subject{Kind: rbac.ServiceAccountKind, Namespace: "dev", Name: "ci"}
	for name, installed := range map[string]*helm.Values{
		"cluster-wide": {},
		"namespaced":   {Namespaces: []string{"a", "b"}},
	} {
		t.Run(name, func(t *testing.T) {
			rq := &ClientRequest{Values: installed, Subjects: []Subject{alice, sa}, Namespaces: []string{"a"}}
			want := generateClient(t, rq)
			rq.Format = FormatValues
			emitted := generateClient(t, rq)
			assert.NotContains(t, string(emitted.Body), "legacyAccess")

			block, err := helm.ParseValues(emitted.Body)
			require.NoError(t, err)
			render, err := RenderChart(ctx, "ambassador", helm.MergeValues(block, installed))
			require.NoError(t, err)
			got, err := ClientManifests(render, "ambassador")
			require.NoError(t, err)
			assert.Equal(t, string(want.Body), string(got))
		})
	}
}

func TestGenerateClient_NoSubject(t *testing.T) {
	_, err := GenerateClient(context.Background(), &ClientRequest{ManagerNamespace: "ambassador", Values: &helm.Values{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--subject")
}

func TestGenerateClient_Golden(t *testing.T) {
	out := generateClient(t, &ClientRequest{Values: &helm.Values{}})
	var buf bytes.Buffer
	_, err := out.WriteTo(&buf)
	require.NoError(t, err)
	got := buf.String()
	if version.Version != "" {
		got = strings.ReplaceAll(got, version.Version, "VERSION")
	}

	path := filepath.Join("testdata", "generate_client_default.golden")
	if *updateGolden {
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), got)
}
