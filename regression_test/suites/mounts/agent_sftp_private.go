package mounts

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AgenthardSftpPrivate proves that a sidecar traffic-agent under enforcing authentication
// serves SFTP only to sessions that came through its tunnel.
type AgenthardSftpPrivate struct {
	rt.Suite
}

func init() {
	rt.Register(&AgenthardSftpPrivate{}, rt.InArea("mounts"), rt.NeedsManager(managers.AuthEnforcing()),
		rt.Requires(rt.FUSE), rt.NotOn("windows"))
}

// sftpInit is the SFTP version-negotiation packet a client sends first (length 5, type
// SSH_FXP_INIT, version 3); a served connection answers with an SSH_FXP_VERSION packet.
const sftpInit = `\0\0\0\5\1\0\0\0\3`

// helperContainer is the container that Template.ReadyDelaySeconds adds to a pod. Its
// image has a shell and nc, which the application image lacks.
const helperContainer = "slow-ready"

// Test_SftpPortClosedToPodContainers mounts a volume of a workload through the tunnel, then
// connects to the traffic-agent's SFTP port from another container of the same pod and sends
// the SFTP init packet. The agent must close the connection without answering, while the
// mount keeps working.
func (s *AgenthardSftpPrivate) Test_SftpPortClosedToPodContainers() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	s.Manager()

	tpl := workloads.EchoWithConfigVolume("agenthard-sftp-private")
	tpl.ReadyDelaySeconds = 1
	wl := s.Workload(tpl)

	fuse := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithConfig(disableFtp)))
	a := fuse.Intercept(t, wl)
	defer a.Detach(t)
	root, ok := rt.MountRoot(a)
	if !ok {
		t.Fatalf("intercept for %s carries no TELEPRESENCE_ROOT", wl.Name)
	}
	t.Cleanup(func() { check.EventuallyRemoved(t, root, mountTimeout) })
	check.EventuallyFile(t, configFilePath(root), isConfigContent, mountTimeout)

	port := s.sftpPort(ctx, ns, wl.Name)

	// The helper must be able to read an answer from a local listener, or an empty
	// answer from the agent would prove nothing.
	out := s.sh(ctx, ns, wl.Name, `(printf pong | nc -l -p 18099 -w 5 >/dev/null &) ; sleep 1; nc -w 3 127.0.0.1 18099 </dev/null | od -An -tx1`)
	s.Require().NotEmpty(out, "the helper container should read an answer from a local listener")

	out = s.sh(ctx, ns, wl.Name, fmt.Sprintf(`printf %q | nc -w 3 127.0.0.1 %d | od -An -tx1`, sftpInit, port))
	s.Empty(out, "the SFTP port answered a connection from another container of the pod (a reply with type 02 at offset 4 is an SFTP version packet)")

	check.EventuallyFile(t, configFilePath(root), isConfigContent, mountTimeout)
}

// sftpPort returns the SFTP port of the traffic-agent that serves the intercept of the
// workload name in ns.
func (s *AgenthardSftpPrivate) sftpPort(ctx context.Context, ns, name string) uint16 {
	var entries []agenthardListEntry
	s.Require().NoError(s.R().CLI().JSON(ctx, &entries, "list", "--format", "json", "-n", ns))
	for _, e := range entries {
		if e.Name != name {
			continue
		}
		for _, ii := range e.InterceptInfo {
			if ii.SftpPort != 0 {
				return uint16(ii.SftpPort)
			}
		}
	}
	s.T().Fatalf("no intercept of %s with an SFTP port in the list output", name)
	return 0
}

// sh runs script in the helper container of the pod of workload name and returns its
// trimmed output.
func (s *AgenthardSftpPrivate) sh(ctx context.Context, ns, name, script string) string {
	s.T().Helper()
	script = "command -v nc >/dev/null && command -v od >/dev/null || exit 3; " + script
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := s.R().Kubectl(cctx, ns, "exec", "deploy/"+name, "-c", helperContainer, "--", "sh", "-c", script)
	s.Require().NoError(err, "running %q in the %s container of %s", script, helperContainer, name)
	return strings.TrimSpace(out)
}
