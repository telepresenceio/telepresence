package mounts

import (
	"context"
	"regexp"
	"strings"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AgenthardFtpLog proves the traffic-agent's log does not record the FTP login of a mount.
type AgenthardFtpLog struct {
	rt.Suite
}

func init() {
	rt.Register(&AgenthardFtpLog{}, rt.InArea("mounts"), rt.NeedsManager(managers.Default),
		rt.Requires(rt.FUSE), rt.NotOn("windows"))
}

var agenthardPassLineRE = regexp.MustCompile(`\bPASS\b`)

// Test_FtpLoginNotLogged mounts an intercepted workload over FTP, with the traffic-manager and
// the traffic-agent logging at debug level, and asserts that the agent's log shows the FTP
// client connecting but carries no PASS line.
func (s *AgenthardFtpLog) Test_FtpLoginNotLogged() {
	t := s.T()
	ctx := s.Ctx()
	s.Manager()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.EchoWithConfigVolume("agenthard-ftp-log"))

	ftp := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithConfig(enableFtp)))
	a := ftp.Intercept(t, wl)
	defer a.Detach(t)
	root, ok := rt.MountRoot(a)
	if !ok {
		t.Fatalf("intercept for %s carries no TELEPRESENCE_ROOT", wl.Name)
	}
	t.Cleanup(func() { check.EventuallyRemoved(t, root, mountTimeout) })
	check.EventuallyFile(t, configFilePath(root), isConfigContent, mountTimeout)

	logs := agenthardAgentLogs(ctx, s.R(), ns, wl.Name)
	s.Require().NotEmpty(logs, "the traffic-agent log of %s should not be empty", wl.Name)
	s.Contains(logs, "Client connected", "the FTP client should have connected to the traffic-agent")
	if loc := agenthardPassLineRE.FindStringIndex(logs); loc != nil {
		t.Errorf("the traffic-agent log contains a PASS line: %q", agenthardLineAround(logs, loc[0]))
	}
}

// agenthardAgentLogs returns the concatenated traffic-agent container logs of the live pods of
// the workload name in ns.
func agenthardAgentLogs(ctx context.Context, r *rt.Runtime, ns, name string) string {
	out, err := r.Kubectl(ctx, ns, "get", "pod", "-l", "app="+name, "-o",
		`jsonpath={range .items[*]}{.metadata.name}|{.spec.containers[*].name}{"\n"}{end}`)
	if err != nil {
		return ""
	}
	var sb strings.Builder
	for _, line := range strings.Split(out, "\n") {
		pod, containers, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok || !strings.Contains(" "+containers+" ", " traffic-agent ") {
			continue
		}
		if logs, err := r.Kubectl(ctx, ns, "logs", pod, "-c", "traffic-agent"); err == nil {
			sb.WriteString(logs)
		}
	}
	return sb.String()
}

// agenthardLineAround returns the line of s that contains offset i, with any token-like value
// replaced so a failure message does not itself leak the secret.
func agenthardLineAround(s string, i int) string {
	start := strings.LastIndexByte(s[:i], '\n') + 1
	end := len(s)
	if nl := strings.IndexByte(s[i:], '\n'); nl >= 0 {
		end = i + nl
	}
	return regexp.MustCompile(`PASS\s+\S+`).ReplaceAllString(s[start:end], "PASS <redacted>")
}
