package nodeagent

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// replaceRefusedSubstring is the error the manager returns for an intercept
// spec that combines NodeAgent and Replace (cmd/traffic/cmd/manager/state/
// intercept.go's PrepareIntercept): node-agent mode never runs the sidecar
// machinery --replace depends on. Reached here via the deprecated
// "intercept --replace" flag combined with --node-agent: the dedicated
// "replace" command never registers a --node-agent flag at all
// (pkg/client/cli/intercept/command.go's validatedRun), so it can't even
// construct this combination.
const replaceRefusedSubstring = "node-agent mode does not support --replace"

// NodeAgentReplace proves that a node-agent attach refuses to also replace
// the application container: PrepareIntercept rejects the combination
// before any Job is created.
type NodeAgentReplace struct {
	rt.Suite
}

func init() {
	rt.Register(&NodeAgentReplace{}, rt.InArea("nodeagent"), rt.NeedsManager(managers.NodeAgent()))
}

// Test_ReplaceRefused proves "intercept --node-agent --replace" fails with
// the manager's refusal message.
func (s *NodeAgentReplace) Test_ReplaceRefused() {
	ctx := s.Ctx()
	s.Connect()
	wl := s.Workload(workloads.Echo("na-replace"))
	ls := s.LocalEcho()

	args := []string{"intercept", wl.Name, "--namespace", wl.Namespace, "--format", "json"}
	for _, o := range []cli.InterceptOpt{rt.ToLocal(ls, "http"), cli.MountFalse(), nodeAgentFlag(), cli.Replace()} {
		args = append(args, o()...)
	}
	stdout, stderr, err := s.CLI().Run(ctx, args...)
	s.Error(err, "a node-agent intercept combined with --replace should be refused")
	s.Contains(stdout+stderr, replaceRefusedSubstring)
}
