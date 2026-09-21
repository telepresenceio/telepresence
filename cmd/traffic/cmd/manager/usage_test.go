package manager

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/usg"
)

func TestManagerBootUsage(t *testing.T) {
	t.Parallel()
	for _, mode := range []auth.Mode{auth.ModeDisabled, auth.ModePermissive, auth.ModeEnforcing} {
		for _, enabled := range []bool{false, true} {
			t.Run(mode.String()+"/"+strconv.FormatBool(enabled), func(t *testing.T) {
				t.Parallel()
				policy := agentconfig.Never
				if enabled {
					policy = agentconfig.OnDemand
				}
				ctx := managerutil.WithEnv(context.Background(), &managerutil.Env{
					AuthenticationMode: mode, NodeAgentEnabled: enabled, AgentInjectPolicy: policy,
				})
				ctx, sink := usg.InstallManager(ctx, "test-installation")
				reportManagerBoot(ctx)
				reports := sink.Drain(0)
				require.Len(t, reports, 1)
				require.Equal(t, "manager.boot", reports[0].Topic)
				require.Equal(t, "manager", reports[0].Source)
				require.Equal(t, map[string]string{
					"authentication.mode": mode.String(),
					"nodeagent.enabled":   strconv.FormatBool(enabled),
					"injector.enabled":    strconv.FormatBool(enabled),
				}, reports[0].Entries)
			})
		}
	}
}

func TestManagerBootUsageWithoutProducer(t *testing.T) {
	t.Parallel()
	ctx := managerutil.WithEnv(context.Background(), &managerutil.Env{AuthenticationMode: auth.ModeEnforcing})
	require.NotPanics(t, func() { reportManagerBoot(ctx) })
}
