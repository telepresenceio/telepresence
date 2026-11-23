package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// agentidMountTimeout bounds each wait for a mounted file to appear or go away.
const agentidMountTimeout = 30 * time.Second

// agentidNodeAgentFlag builds --node-agent.
func agentidNodeAgentFlag() cli.InterceptOpt {
	return func() []string { return []string{"--node-agent"} }
}

// agentidUseFtp is a ConnWithConfig option selecting the FTP (true) or the
// SFTP (false) mount transport.
func agentidUseFtp(useFtp bool) rt.ConnOpt {
	return rt.ConnWithConfig(func(c client.Config) { c.Intercept().UseFtp = useFtp })
}

// agentidMountedIntercept intercepts wl with mounts enabled over a fresh
// connection that uses the given mount transport, as the framework's default
// (granted) identity. It asserts that the intercept routes the workload's
// traffic to ls, that the ConfigMap file and the service account token are
// readable through the mount, and then detaches and waits for the mount
// directory to go away.
func agentidMountedIntercept(t *testing.T, ns string, wl *rt.Workload, ls *rt.LocalService, useFtp bool, opts ...cli.InterceptOpt) {
	t.Helper()
	conn := rt.Mutate(t, rt.ConnectionFixture(ns, agentidUseFtp(useFtp)))
	a := conn.Intercept(t, wl, append([]cli.InterceptOpt{rt.ToLocal(ls, "http")}, opts...)...)
	detached := false
	t.Cleanup(func() {
		if !detached {
			a.Detach(t)
		}
	})

	root, ok := rt.MountRoot(a)
	if !ok {
		t.Fatalf("intercept for %s carries no TELEPRESENCE_ROOT", wl.Name)
	}
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
	check.EventuallyFile(t, filepath.Join(root, workloads.ConfigVolumeMountPath, workloads.ConfigVolumeFileName),
		func(b []byte) bool { return string(b) == workloads.ConfigVolumeContent }, agentidMountTimeout)
	check.EventuallyFile(t, filepath.Join(root, "var/run/secrets/kubernetes.io/serviceaccount/token"),
		func(b []byte) bool { return len(b) > 0 }, agentidMountTimeout)

	a.Detach(t)
	detached = true
	check.EventuallyRemoved(t, root, agentidMountTimeout)
}

// agentidAgentLogs returns the log of the traffic-agent container of the
// Deployment name in ns.
func agentidAgentLogs(ctx context.Context, r *rt.Runtime, ns, name string) (string, error) {
	return r.Kubectl(ctx, ns, "logs", "deploy/"+name, "-c", "traffic-agent", "--tail=-1")
}
