package helm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// testConfigContext returns a context carrying the given YAML client config and an
// empty Env, which client.Images.Registry and friends read without a nil dereference.
func testConfigContext(t *testing.T, configYAML string) context.Context {
	t.Helper()
	cfg, err := client.ParseConfigYAML(t.Context(), "test", []byte(configYAML))
	require.NoError(t, err)
	ctx := client.WithEnv(t.Context(), &client.Env{})
	return client.WithConfig(ctx, cfg)
}

func TestGetValues(t *testing.T) {
	t.Run("image registry and tag from client config", func(t *testing.T) {
		ctx := testConfigContext(t, `
images:
  registry: testregistry.io
`)
		vs := GetValues(ctx, &Request{Version: "2.99.0"})
		assert.Equal(t, "testregistry.io", *vs.Image.Registry)
		assert.Equal(t, "2.99.0", *vs.Image.Tag)
		assert.Zero(t, vs.Grpc)
		assert.Zero(t, vs.Agent)
	})

	t.Run("version defaults to the client's own version", func(t *testing.T) {
		ctx := testConfigContext(t, ``)
		vs := GetValues(ctx, &Request{})
		assert.Equal(t, client.Version(), "v"+*vs.Image.Tag)
	})

	t.Run("grpc max receive size set only when non-zero", func(t *testing.T) {
		ctx := testConfigContext(t, `
grpc:
  maxReceiveSize: 10Mi
`)
		vs := GetValues(ctx, &Request{Version: "2.99.0"})
		require.NotNil(t, vs.Grpc.MaxReceiveSize)
		assert.Equal(t, "10Mi", vs.Grpc.MaxReceiveSize.String())
	})

	t.Run("agent image split into name, tag and registry", func(t *testing.T) {
		ctx := testConfigContext(t, `
images:
  agentImage: myregistry.io/tel-agent:1.2.3
`)
		vs := GetValues(ctx, &Request{Version: "2.99.0"})
		assert.Equal(t, "tel-agent", *vs.Agent.Image.Name)
		assert.Equal(t, "1.2.3", *vs.Agent.Image.Tag)
		assert.Equal(t, "myregistry.io", *vs.Agent.Image.Registry)
	})

	t.Run("agent image without registry keeps registry unset", func(t *testing.T) {
		ctx := testConfigContext(t, `
images:
  agentImage: tel-agent:1.2.3
`)
		vs := GetValues(ctx, &Request{Version: "2.99.0"})
		assert.Equal(t, "tel-agent", *vs.Agent.Image.Name)
		assert.Equal(t, "1.2.3", *vs.Agent.Image.Tag)
		assert.Nil(t, vs.Agent.Image.Registry)
	})
}

func TestGetTrafficManagerVersion(t *testing.T) {
	t.Run("no image tag set falls back to the client's own version", func(t *testing.T) {
		v, err := (&Values{}).TrafficManagerVersion()
		require.NoError(t, err)
		assert.Equal(t, version.Structured, v)
	})

	t.Run("valid image tag is accepted", func(t *testing.T) {
		tag := "2.99.0"
		v, err := (&Values{Image: Image{Tag: &tag}}).TrafficManagerVersion()
		require.NoError(t, err)
		assert.Equal(t, version.Structured, v)
	})

	t.Run("invalid image tag is an error", func(t *testing.T) {
		tag := "not-a-version!"
		_, err := (&Values{Image: Image{Tag: &tag}}).TrafficManagerVersion()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "image.tag")
	})
}
