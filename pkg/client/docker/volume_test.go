package docker

import (
	"context"
	"testing"

	"github.com/blang/semver"
	"github.com/stretchr/testify/require"
)

func Test_getLatestPluginVersion(t *testing.T) {
	ver, err := getLatestPluginVersion(context.Background())
	require.NoError(t, err)
	require.True(t, semver.MustParse("0.1.3").LT(ver))
}
