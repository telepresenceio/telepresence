package integration_test

import (
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func (s *singleServiceSuite) Test_LegacySwapDeploymentDoesIntercept() {
	require := s.Require()
	ctx := s.Context()

	// We don't need to defer leaving the intercept because the
	// intercept is automatically left once the command is finished
	_, stderr, err := itest.Telepresence(ctx, "--swap-deployment", s.ServiceName(), "--expose", "9090",
		"--mount", "false", "--run", "sleep", "1")
	require.NoError(err)
	require.Contains(stderr, "Legacy Telepresence command used")
	require.Contains(stderr, "Using Deployment "+s.ServiceName())

	// Since legacy Telepresence commands are detected and translated in the
	// RunSubcommands function, so we ensure that the help text is *not* being
	// printed out in this case.
	require.NotContains(stderr, "Telepresence can connect to a cluster and route all outbound traffic")

	// Verify that the intercept no longer exists
	s.Eventually(func() bool {
		stdout, stderr, err := itest.Telepresence(ctx, "list", "--intercepts")
		if err != nil || stderr != "" {
			return false
		}
		return strings.Contains(stdout, "No Workloads (Deployments, StatefulSets, or ReplicaSets)")
	},
		10*time.Second,
		1*time.Second,
	)
}
