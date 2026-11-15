package injector

import (
	"slices"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

const (
	// arrivalInitDelaySeconds is how long each new pod's init container
	// runs. It exceeds the manager's agent arrival timeout, so no agent
	// arrives in time.
	arrivalInitDelaySeconds = 20
	arrivalSettleTimeout    = 3 * time.Minute
	// arrivalQuietPeriod is longer than two init delays.
	arrivalQuietPeriod = 45 * time.Second
	arrivalPollEvery   = 3 * time.Second
)

// ArrivalTimeout proves that the traffic-manager stops replacing a
// workload's pods after the traffic-agent failed to arrive in time.
type ArrivalTimeout struct {
	rt.Suite
}

func init() {
	rt.Register(&ArrivalTimeout{},
		rt.InArea("injector"),
		rt.NeedsManager(managers.AgentArrivalTimeout("5s")),
	)
}

// Test_PodsSettleAfterArrivalTimeout intercepts a three-replica Deployment
// whose pods take longer to start than the arrival timeout. The intercept
// fails. Once the pods are back without an agent, their names must not
// change anymore.
func (s *ArrivalTimeout) Test_PodsSettleAfterArrivalTimeout() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Connect()

	tpl := workloads.EchoReplicas("arrival-timeout", 3)
	tpl.InitDelaySeconds = arrivalInitDelaySeconds
	// The test leaves the workload's pods restarted, so it must not be
	// memoized for other suites.
	wl := freshWorkload(t, ctx, r, ns, tpl)
	ls := s.LocalEcho()

	args := []string{"intercept", wl.Name, "--namespace", ns}
	for _, o := range []cli.InterceptOpt{rt.ToLocal(ls, "http"), cli.MountFalse()} {
		args = append(args, o()...)
	}
	stdout, stderr, err := s.CLI().Run(ctx, args...)
	s.Require().Error(err, "the intercept should fail: %s", stdout)
	s.Contains(stdout+stderr, "timed out while waiting for agent")

	var names []string
	s.Require().Eventually(func() bool {
		pods, err := podsOf(ctx, r, ns, wl.Name)
		if err != nil || len(pods) != tpl.Replicas {
			return false
		}
		names = names[:0]
		for i := range pods {
			if !podReady(&pods[i]) || podHasAgent(&pods[i]) {
				return false
			}
			names = append(names, pods[i].Name)
		}
		slices.Sort(names)
		return true
	}, arrivalSettleTimeout, agentPollInterval, "every pod should be ready and without a traffic-agent")

	deadline := time.Now().Add(arrivalQuietPeriod)
	for time.Now().Before(deadline) {
		time.Sleep(arrivalPollEvery)
		pods, err := podsOf(ctx, r, ns, wl.Name)
		s.Require().NoError(err)
		current := make([]string, len(pods))
		for i := range pods {
			current[i] = pods[i].Name
		}
		slices.Sort(current)
		s.Require().Equal(names, current, "pods %s must not be replaced after the arrival timeout", strings.Join(names, ","))
	}
}
