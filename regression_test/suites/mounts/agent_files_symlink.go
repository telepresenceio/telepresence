package mounts

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/pkg/sftp"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AgenthardSftp proves the SFTP server of a node-hosted traffic-agent keeps a client inside the
// files the agent exports.
type AgenthardSftp struct {
	rt.Suite
}

func init() {
	rt.Register(&AgenthardSftp{}, rt.InArea("mounts"), rt.NeedsManager(managers.NodeAgent()),
		rt.Requires(rt.FUSE), rt.NotOn("windows"))
}

const (
	agenthardPodWait      = 30 * time.Second
	agenthardExports      = "/tel_app_exports"
	agenthardSftpDeadline = 2 * time.Minute
	agenthardWalkDepth    = 8
)

var agenthardProcRootRE = regexp.MustCompile(`^(/proc/\d+/root)(/|$)`)

// agenthardListEntry mirrors the parts of a `list --format json` entry that locate the
// traffic-agent of an intercept.
type agenthardListEntry struct {
	Name          string `json:"name"`
	InterceptInfo []struct {
		PodName  string `json:"pod_name"`
		PodIP    string `json:"pod_ip"`
		SftpPort int32  `json:"sftp_port"`
		FtpPort  int32  `json:"ftp_port"`
	} `json:"intercept_info"`
}

// Test_ClientLinkDoesNotLeaveExport holds a node-agent intercept with a mount and talks SFTP to
// the traffic-agent the way the client's mount does. It tries to create a link that points at
// the root file system of the intercepted container, and then to read and to write through
// it. The link must be refused, and neither the read nor the write may succeed.
func (s *AgenthardSftp) Test_ClientLinkDoesNotLeaveExport() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	s.Manager()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.EchoWithConfigVolume("agenthard-sftp"))

	fuse := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithConfig(disableFtp)))
	a := fuse.Intercept(t, wl, nodeAgentFlag())
	defer a.Detach(t)
	root, ok := rt.MountRoot(a)
	if !ok {
		t.Fatalf("intercept for %s carries no TELEPRESENCE_ROOT", wl.Name)
	}
	t.Cleanup(func() { check.EventuallyRemoved(t, root, mountTimeout) })
	check.EventuallyFile(t, tokenFilePath(root), isNonEmpty, mountTimeout)

	agentNS, agentPod, sftpPort, _ := agenthardLocateAgent(ctx, s, ns, wl.Name)
	dial, err := rt.AgenthardPodDialer(rt.Env{Ctx: ctx, T: t, R: r})
	s.Require().NoError(err)
	conn, err := dial(ctx, agentNS, agentPod, sftpPort)
	s.Require().NoError(err, "dialing the SFTP port of %s.%s", agentPod, agentNS)
	watchdog := time.AfterFunc(agenthardSftpDeadline, func() { _ = conn.Close() })
	defer watchdog.Stop()
	sc, err := sftp.NewClientPipe(conn, conn)
	s.Require().NoError(err, "starting the SFTP client")
	defer sc.Close()

	exports, err := sc.ReadDir(agenthardExports)
	s.Require().NoError(err, "listing %s", agenthardExports)
	s.Require().NotEmpty(exports, "%s should list the container directories", agenthardExports)
	cnDir := path.Join(agenthardExports, exports[0].Name())

	procRoot := agenthardFindProcRoot(sc, cnDir, 0)
	s.Require().NotEmpty(procRoot, "an exported link into /proc/<pid>/root should exist under %s", cnDir)

	id := fmt.Sprint(time.Now().UnixNano())
	link := path.Join(cnDir, "agenthard-link-"+id)
	if err := sc.Symlink(procRoot, link); err == nil {
		t.Errorf("creating a link to %s was accepted", procRoot)
		defer func() { _ = sc.Remove(link) }()
	}

	if f, err := sc.Open(path.Join(link, "etc/hosts")); err == nil {
		data, _ := io.ReadAll(io.LimitReader(f, 64))
		_ = f.Close()
		t.Errorf("read %d bytes of the container's /etc/hosts through the link", len(data))
	}

	outside := path.Join(link, "agenthard-outside-"+id)
	if f, err := sc.OpenFile(outside, os.O_WRONLY|os.O_CREATE|os.O_EXCL); err == nil {
		_, _ = f.Write([]byte("agenthard"))
		_ = f.Close()
		_ = sc.Remove(outside)
		t.Errorf("created %s through the link", outside)
	}

	if info, err := sc.Stat(path.Join(link, "agenthard-outside-"+id)); err == nil {
		t.Errorf("the file written through the link exists outside the export: %v", info.Name())
	}
}

// agenthardLocateAgent returns the namespace, name, SFTP port and FTP port of the traffic-agent
// pod that serves the intercept of the workload name in ns.
func agenthardLocateAgent(ctx context.Context, s *AgenthardSftp, ns, name string) (agentNS, pod string, port, ftpPort uint16) {
	t := s.T()
	var entries []agenthardListEntry
	s.Require().NoError(s.R().CLI().JSON(ctx, &entries, "list", "--format", "json", "-n", ns))
	var podName, podIP string
	for _, e := range entries {
		if e.Name != name {
			continue
		}
		for _, ii := range e.InterceptInfo {
			if ii.SftpPort != 0 {
				podName, podIP, port, ftpPort = ii.PodName, ii.PodIP, uint16(ii.SftpPort), uint16(ii.FtpPort)
			}
		}
	}
	if port == 0 {
		t.Fatalf("no intercept of %s with an SFTP port in the list output", name)
	}
	start := time.Now()
	for {
		agentNS, pod = agenthardFindPod(ctx, s, podName, podIP)
		if pod != "" {
			break
		}
		if time.Since(start) >= agenthardPodWait || ctx.Err() != nil {
			t.Fatalf("no pod with IP %s found for the traffic-agent of %s within %s", podIP, name, agenthardPodWait)
		}
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}
	return agentNS, pod, port, ftpPort
}

// agenthardFindPod returns the namespace and name of the pod with the given IP, preferring the
// pod named podName, or "" when no pod has that IP.
func agenthardFindPod(ctx context.Context, s *AgenthardSftp, podName, podIP string) (agentNS, pod string) {
	out, err := s.R().Kubectl(ctx, "", "get", "pods", "-A", "-o",
		`jsonpath={range .items[*]}{.metadata.namespace}|{.metadata.name}|{.status.podIP}{"\n"}{end}`)
	if err != nil {
		if ctx.Err() == nil {
			s.Require().NoError(err)
		}
		return "", ""
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
		if len(parts) != 3 || parts[2] != podIP {
			continue
		}
		if parts[1] == podName || agentNS == "" {
			agentNS, pod = parts[0], parts[1]
		}
	}
	return agentNS, pod
}

// agenthardFindProcRoot returns the /proc/<pid>/root prefix of the first link under dir whose
// target lies in a container's procfs root, or "" when there is none.
func agenthardFindProcRoot(sc *sftp.Client, dir string, depth int) string {
	if depth > agenthardWalkDepth {
		return ""
	}
	entries, err := sc.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		p := path.Join(dir, e.Name())
		switch {
		case e.Mode()&os.ModeSymlink != 0:
			if target, err := sc.ReadLink(p); err == nil {
				if m := agenthardProcRootRE.FindStringSubmatch(target); m != nil {
					return m[1]
				}
			}
		case e.IsDir():
			if root := agenthardFindProcRoot(sc, p, depth+1); root != "" {
				return root
			}
		}
	}
	return ""
}
