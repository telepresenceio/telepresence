package rbacgen

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
)

func kindNames(r *Render) (objects, hooks []string) {
	for _, o := range r.Objects {
		objects = append(objects, o.GetKind()+" "+o.GetName())
	}
	for _, o := range r.Hooks {
		hooks = append(hooks, o.GetKind()+" "+o.GetName())
	}
	return objects, hooks
}

func TestRenderChart_HooksKeptApart(t *testing.T) {
	r, err := RenderChart(context.Background(), "ambassador", &helm.Values{
		AgentInjector: helm.AgentInjector{Enabled: new(true)},
	})
	require.NoError(t, err)
	objects, hooks := kindNames(r)

	assert.Contains(t, objects, "StatefulSet traffic-manager")
	assert.Contains(t, hooks, "Job uninstall-agents")
	assert.Contains(t, hooks, "Job traffic-manager-migrate-statefulset")
	assert.Contains(t, hooks, "Role traffic-manager-migrate-hook")
	for _, h := range hooks {
		assert.NotContains(t, objects, h)
	}
	assert.Len(t, r.All(), len(r.Objects)+len(r.Hooks))
}

func TestDecodeManifests(t *testing.T) {
	objs, err := decodeManifests("---\n# only a comment\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n---\n\n")
	require.NoError(t, err)
	require.Len(t, objs, 1)
	assert.Equal(t, "ConfigMap", objs[0].GetKind())

	_, err = decodeManifests("kind: [")
	assert.Error(t, err)
}
