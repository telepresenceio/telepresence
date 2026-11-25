package mounts

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AgenthardSftpPrivate proves that a sidecar traffic-agent under enforcing authentication
// serves SFTP only through its tunnel.
type AgenthardSftpPrivate struct {
	rt.Suite
}

// AgenthardSftpPrivatePermissive proves the same under permissive authentication.
type AgenthardSftpPrivatePermissive struct {
	rt.Suite
}

func init() {
	rt.Register(&AgenthardSftpPrivate{}, rt.InArea("mounts"), rt.NeedsManager(managers.AuthEnforcing()),
		rt.Requires(rt.FUSE), rt.NotOn("windows"))
	rt.Register(&AgenthardSftpPrivatePermissive{}, rt.InArea("mounts"), rt.NeedsManager(managers.AuthPermissive()),
		rt.Requires(rt.FUSE), rt.NotOn("windows"))
}

// sftpInit is the SFTP version-negotiation packet a client sends first (length 5, type
// SSH_FXP_INIT, version 3); a served connection answers with an SSH_FXP_VERSION packet.
const sftpInit = `\0\0\0\5\1\0\0\0\3`

// helperContainer is the container that Template.ReadyDelaySeconds adds to a pod. Its
// image has a shell, nc, od and curl, which the application image lacks.
const helperContainer = "slow-ready"

// Test_SftpPortClosedToPodContainers mounts a volume of a workload through the tunnel, then
// connects to the traffic-agent's SFTP port from another container of the same pod and sends
// the SFTP init packet. The agent must close the connection without answering, while the
// mount keeps working.
func (s *AgenthardSftpPrivate) Test_SftpPortClosedToPodContainers() {
	requireSftpPortClosedToPodContainers(&s.Suite, "agenthard-sftp-private")
}

// Test_SftpPortClosedToPodContainers is the same check against a manager that does not
// enforce authentication.
func (s *AgenthardSftpPrivatePermissive) Test_SftpPortClosedToPodContainers() {
	requireSftpPortClosedToPodContainers(&s.Suite, "agenthard-sftp-permissive")
}

func requireSftpPortClosedToPodContainers(s *rt.Suite, workload string) {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	s.Manager()

	tpl := workloads.EchoWithConfigVolume(workload)
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

	pod, port := sftpAgent(s, ctx, ns, wl.Name)

	// The helper must be able to read an answer from a local listener, or an empty
	// answer from the agent would prove nothing.
	res := probePort(s, ctx, ns, pod, `(printf pong | nc -l -p 18099 -w 5 >/dev/null &); sleep 1`, 18099)
	s.Require().Equal(0, res.rc, "the control probe should connect to a local listener")
	s.Require().NotEmpty(res.hex, "the helper container should read an answer from a local listener")

	// Exit status 7 is a refused connection. The others listed mean it was established:
	// the peer closed (0, 52, 56) or kept it open past curl's time limit (28).
	res = probePort(s, ctx, ns, pod, "", port)
	s.Require().Contains([]int{0, 28, 52, 56}, res.rc, "the helper container could not connect to port %d of pod %s (curl exit status %d)", port, pod, res.rc)
	s.Empty(res.hex, "the SFTP port answered a connection from another container of the pod (a reply with type 02 at offset 4 is an SFTP version packet)")

	check.EventuallyFile(t, configFilePath(root), isConfigContent, mountTimeout)
}

// sftpAgent returns the name of the pod that runs the traffic-agent of the intercept of the
// workload name in ns, and the agent's SFTP port.
func sftpAgent(s *rt.Suite, ctx context.Context, ns, name string) (string, uint16) {
	var entries []agenthardListEntry
	s.Require().NoError(s.R().CLI().JSON(ctx, &entries, "list", "--format", "json", "-n", ns))
	for _, e := range entries {
		if e.Name != name {
			continue
		}
		for _, ii := range e.InterceptInfo {
			if ii.SftpPort != 0 && ii.PodName != "" {
				return ii.PodName, uint16(ii.SftpPort)
			}
		}
	}
	s.T().Fatalf("no intercept of %s with an SFTP port and a pod in the list output", name)
	return "", 0
}

// probeResult is the outcome of probePort: curl's exit status and the hex dump of what the
// peer sent.
type probeResult struct {
	rc  int
	hex string
}

// probePort runs setup, then connects to 127.0.0.1:port from the helper container of pod and
// sends the SFTP init packet.
func probePort(s *rt.Suite, ctx context.Context, ns, pod, setup string, port uint16) probeResult {
	s.T().Helper()
	script := fmt.Sprintf(`command -v nc >/dev/null && command -v od >/dev/null || exit 3; %s
printf %q | curl -sS --max-time 3 -T - telnet://127.0.0.1:%d -o /tmp/probe.out 2>/dev/null
echo "rc=$?"; od -An -tx1 /tmp/probe.out`, setup, sftpInit, port)
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := s.R().Kubectl(cctx, ns, "exec", pod, "-c", helperContainer, "--", "sh", "-c", script)
	s.Require().NoError(err, "running the probe in the %s container of pod %s", helperContainer, pod)
	first, rest, _ := strings.Cut(strings.TrimSpace(out), "\n")
	rc, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(first), "rc="))
	s.Require().NoError(err, "unexpected probe output %q", out)
	return probeResult{rc: rc, hex: strings.TrimSpace(rest)}
}
