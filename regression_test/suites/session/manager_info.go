package session

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// ManagerInfo proves the manager's cluster-info RPC (the "GetClusterInfo"
// the wave-3 spec refers to; the manager has no unary GetClusterInfo, only
// the streaming WatchClusterInfo) reports a service subnet consistent with
// what a connected client's `status --format json` actually routes. Carries
// CompatCore: Test_ServiceSubnetMatchesStatus exercises WatchClusterInfo
// directly; see framework/compat/manifest.go.
type ManagerInfo struct {
	rt.Suite
}

func init() {
	rt.Register(&ManagerInfo{},
		rt.InArea("session"),
		rt.NeedsManager(managers.Default),
		rt.WithLabels(rt.CompatCore),
	)
}

func (s *ManagerInfo) Test_ServiceSubnetMatchesStatus() {
	t := s.T()
	ctx := s.Ctx()
	s.Connect()

	env := rt.Env{Ctx: ctx, T: t, R: s.R()}
	mc, closeMC, err := rt.ManagerClient(env, managers.ManagerNamespace)
	s.Require().NoError(err)
	defer closeMC()

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sessionInfo := arriveAsClient(t, watchCtx, mc, "rtest-managerinfo", s.AppNamespace())

	stream, err := mc.WatchClusterInfo(watchCtx, sessionInfo)
	s.Require().NoError(err)
	info, err := stream.Recv()
	s.Require().NoError(err)
	s.Require().NotNil(info.GetServiceSubnet(), "manager did not report a service subnet")

	serviceSubnet := iputil.RPCToPrefix(info.GetServiceSubnet())
	s.True(serviceSubnet.IsValid(), "manager reported an unparsable service subnet")

	var st daemonStatus
	s.Require().NoError(s.CLI().JSON(ctx, &st, "status", "--format", "json"))

	s.True(subnetContainsPrefix(st.RootDaemon.Subnets, serviceSubnet),
		"manager service subnet %s not reflected in status subnets %v", serviceSubnet, st.RootDaemon.Subnets)
}
