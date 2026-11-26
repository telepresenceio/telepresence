package rbacgen

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	auth "k8s.io/api/authorization/v1"
	rbac "k8s.io/api/rbac/v1"
)

func TestPolicyRules(t *testing.T) {
	rules := PolicyRules([]*auth.ResourceAttributes{
		{Verb: "create", Resource: "configmaps", Namespace: "ns", Name: "a"},
		{Verb: "get", Resource: "configmaps", Namespace: "ns", Name: "b"},
		{Verb: "get", Resource: "configmaps", Namespace: "ns", Name: "a"},
		{Verb: "delete", Resource: "configmaps", Namespace: "ns", Name: "a"},
		{Verb: "delete", Resource: "configmaps", Namespace: "ns", Name: "b"},
		{Verb: "list", Resource: "secrets", Namespace: "ns"},
		{Verb: "get", Resource: "secrets", Namespace: "ns", Name: "s"},
		{Verb: "get", Resource: "secrets", Namespace: "ns"},
		{Verb: "create", Resource: "pods", Subresource: "eviction"},
	})
	assert.Equal(t, []rbac.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"create"}},
		{APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: []string{"a", "b"}, Verbs: []string{"delete", "get"}},
		{APIGroups: []string{""}, Resources: []string{"pods/eviction"}, Verbs: []string{"create"}},
		{APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get", "list"}},
	}, rules)
}

func TestManifests_Empty(t *testing.T) {
	data, err := Manifests("x", nil, nil)
	require.NoError(t, err)
	assert.Empty(t, data)
}

func TestParseSubject(t *testing.T) {
	for in, want := range map[string]Subject{
		"User:alice":                {Kind: "User", Name: "alice"},
		"Group:devs":                {Kind: "Group", Name: "devs"},
		"ServiceAccount:dev/deploy": {Kind: "ServiceAccount", Namespace: "dev", Name: "deploy"},
	} {
		got, err := ParseSubject(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got)
		assert.Equal(t, in, got.String())
	}
	for _, in := range []string{"alice", "User:", "ServiceAccount:deploy", "ServiceAccount:/deploy", "Robot:x"} {
		_, err := ParseSubject(in)
		assert.Error(t, err, in)
	}
}

func TestIdentity(t *testing.T) {
	sa := &Identity{Username: "system:serviceaccount:dev:deploy", Groups: []string{"system:serviceaccounts"}}
	assert.Equal(t, Subject{Kind: "ServiceAccount", Namespace: "dev", Name: "deploy"}, sa.Subject())
	assert.True(t, sa.Matches(Subject{Kind: "ServiceAccount", Namespace: "dev", Name: "deploy"}))
	assert.False(t, sa.Matches(Subject{Kind: "ServiceAccount", Namespace: "other", Name: "deploy"}))
	assert.False(t, sa.Matches(Subject{Kind: "User", Name: "deploy"}))
	assert.True(t, sa.Matches(Subject{Kind: "Group", Name: "system:serviceaccounts"}))

	user := &Identity{Username: "alice", Groups: []string{"devs"}}
	assert.Equal(t, Subject{Kind: "User", Name: "alice"}, user.Subject())
	assert.True(t, user.Matches(Subject{Kind: "User", Name: "alice"}))
	assert.True(t, user.Matches(Subject{Kind: "Group", Name: "devs"}))
	assert.False(t, user.Matches(Subject{Kind: "Group", Name: "ops"}))
}

func TestParseVerbs(t *testing.T) {
	v, err := ParseVerbs("lifecycle")
	require.NoError(t, err)
	assert.Equal(t, VerbsLifecycle, v)
	_, err = ParseVerbs("upgrade")
	assert.Error(t, err)
}
