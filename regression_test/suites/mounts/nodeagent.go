package mounts

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// nodeAgentFlag builds --node-agent, requesting a node-hosted traffic-agent Job instead of
// an injected sidecar for the attach. Duplicated from suites/nodeagent/helpers.go: suite
// packages don't share private helpers.
func nodeAgentFlag() cli.InterceptOpt {
	return func() []string { return []string{"--node-agent"} }
}

// NodeAgentContent proves a mounts-enabled intercept over a node-hosted traffic-agent
// serves the same content a sidecar intercept does (see Content), under both
// intercept.useFtp variants (see FTPvsFUSE): the confined file servers must follow the
// node-agent's symlinks into the intercepted container's filesystem, not just the
// sidecar's fixed mounts tree.
type NodeAgentContent struct {
	rt.Suite
}

func init() {
	rt.Register(&NodeAgentContent{}, rt.InArea("mounts"), rt.NeedsManager(managers.NodeAgent()),
		rt.Requires(rt.FUSE), rt.NotOn("windows"))
}

// Test_ConfigMapContentOverNodeAgent intercepts EchoWithConfigVolume over a node-agent Job
// with mounts enabled (no --mount false), asserting the mounted ConfigMap file's content
// and the serviceaccount token file -- the same checks Content.Test_ConfigMapContent makes
// for a sidecar -- under both intercept.useFtp variants, one connection at a time
// (FTPvsFUSE.Test_ContentUnderFTPAndFUSE's pattern).
func (s *NodeAgentContent) Test_ConfigMapContentOverNodeAgent() {
	t := s.T()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.EchoWithConfigVolume("mounts-nodeagent-content"))

	ftp := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithConfig(enableFtp)))
	a := ftp.Intercept(t, wl, nodeAgentFlag())
	rootFtp, ok := rt.MountRoot(a)
	if !ok {
		t.Fatalf("intercept for %s carries no TELEPRESENCE_ROOT", wl.Name)
	}
	check.EventuallyFile(t, configFilePath(rootFtp), isConfigContent, mountTimeout)
	check.EventuallyFile(t, tokenFilePath(rootFtp), isNonEmpty, mountTimeout)
	a.Detach(t)
	check.EventuallyRemoved(t, rootFtp, mountTimeout)

	fuse := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithConfig(disableFtp)))
	b := fuse.Intercept(t, wl, nodeAgentFlag())
	defer b.Detach(t)
	rootFuse, ok := rt.MountRoot(b)
	if !ok {
		t.Fatalf("intercept for %s carries no TELEPRESENCE_ROOT", wl.Name)
	}
	t.Cleanup(func() { check.EventuallyRemoved(t, rootFuse, mountTimeout) })
	check.EventuallyFile(t, configFilePath(rootFuse), isConfigContent, mountTimeout)
	check.EventuallyFile(t, tokenFilePath(rootFuse), isNonEmpty, mountTimeout)
}
