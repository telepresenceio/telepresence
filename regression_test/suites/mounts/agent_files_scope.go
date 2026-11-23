package mounts

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"

	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// agenthardFtp is a bare FTP control connection to a traffic-agent. Only commands that need no
// data connection are used.
type agenthardFtp struct {
	conn net.Conn
	rd   *bufio.Reader
}

// agenthardFtpLogin connects to the FTP port of a traffic-agent and logs in. The node-agent
// manager runs in permissive mode, which accepts the placeholder password.
func agenthardFtpLogin(ctx context.Context, s *AgenthardSftp, agentNS, agentPod string, port uint16) *agenthardFtp {
	t := s.T()
	dial, err := rt.AgenthardPodDialer(rt.Env{Ctx: ctx, T: t, R: s.R()})
	s.Require().NoError(err)
	conn, err := dial(ctx, agentNS, agentPod, port)
	s.Require().NoError(err, "dialing the FTP port of %s.%s", agentPod, agentNS)
	watchdog := time.AfterFunc(agenthardSftpDeadline, func() { _ = conn.Close() })
	t.Cleanup(func() {
		watchdog.Stop()
		_ = conn.Close()
	})
	f := &agenthardFtp{conn: conn, rd: bufio.NewReader(conn)}
	code, text := f.reply()
	s.Require().Equal(220, code, "FTP banner: %s", text)
	code, text = f.cmd("USER anonymous")
	s.Require().Equal(331, code, "FTP USER: %s", text)
	code, text = f.cmd("PASS anonymous")
	s.Require().Equal(230, code, "FTP PASS: %s", text)
	return f
}

// reply reads one, possibly multi-line, FTP reply. A broken connection yields code 0.
func (f *agenthardFtp) reply() (int, string) {
	var first string
	for {
		line, err := f.rd.ReadString('\n')
		if err != nil {
			return 0, strings.TrimSpace(line + err.Error())
		}
		line = strings.TrimRight(line, "\r\n")
		if len(line) < 4 {
			continue
		}
		if first == "" {
			first = line
		}
		if line[3] == ' ' {
			code, _ := strconv.Atoi(line[:3])
			return code, first
		}
	}
}

func (f *agenthardFtp) cmd(format string, args ...any) (int, string) {
	if _, err := fmt.Fprintf(f.conn, format+"\r\n", args...); err != nil {
		return 0, err.Error()
	}
	return f.reply()
}

// succeeded reports whether code is a positive completion reply.
func ftpSucceeded(code int) bool {
	return code >= 200 && code < 300
}

// agenthardAttachWithMount intercepts wl over a node-agent with a mount and returns an SFTP
// client and, when withFtp is set, a logged-in FTP connection to the traffic-agent, plus the
// name of the container's directory in the exports tree.
func (s *AgenthardSftp) agenthardAttachWithMount(wl *rt.Workload) (*rt.Attach, *sftp.Client, *agenthardFtp, string) {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()

	fuse := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithConfig(disableFtp)))
	a := fuse.Intercept(t, wl, nodeAgentFlag())
	root, ok := rt.MountRoot(a)
	if !ok {
		a.Detach(t)
		t.Fatalf("intercept for %s carries no TELEPRESENCE_ROOT", wl.Name)
	}
	// Cleanups run last to first: detach, then wait for the mount to go away.
	t.Cleanup(func() { check.EventuallyRemoved(t, root, mountTimeout) })
	t.Cleanup(func() { a.Detach(t) })
	check.EventuallyFile(t, tokenFilePath(root), isNonEmpty, mountTimeout)

	agentNS, agentPod, sftpPort, ftpPort := agenthardLocateAgent(ctx, s, ns, wl.Name)
	s.Require().NotZero(ftpPort, "the traffic-agent of %s should have an FTP port", wl.Name)
	dial, err := rt.AgenthardPodDialer(rt.Env{Ctx: ctx, T: t, R: s.R()})
	s.Require().NoError(err)
	conn, err := dial(ctx, agentNS, agentPod, sftpPort)
	s.Require().NoError(err, "dialing the SFTP port of %s.%s", agentPod, agentNS)
	watchdog := time.AfterFunc(agenthardSftpDeadline, func() { _ = conn.Close() })
	t.Cleanup(func() { watchdog.Stop() })
	sc, err := sftp.NewClientPipe(conn, conn)
	s.Require().NoError(err, "starting the SFTP client")
	t.Cleanup(func() { _ = sc.Close() })

	exports, err := sc.ReadDir(agenthardExports)
	s.Require().NoError(err, "listing %s", agenthardExports)
	s.Require().NotEmpty(exports, "%s should list the container directories", agenthardExports)
	return a, sc, agenthardFtpLogin(ctx, s, agentNS, agentPod, ftpPort), exports[0].Name()
}

func sftpReadAll(sc *sftp.Client, p string) (string, error) {
	f, err := sc.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1024))
	return string(b), err
}

// Test_VolumeBelowSharedDirNotExported intercepts, over a node-agent with a mount, a workload
// that shares a volume at /rtest-data and has a ConfigMap volume with the ignore policy
// mounted below it. The traffic-agent reaches the container's file system through
// /proc/<pid>/root, where the nested mount is visible, so it must not export /rtest-data at
// all. Over SFTP and over FTP, neither the nested file nor the parent directory may be reachable.
func (s *AgenthardSftp) Test_VolumeBelowSharedDirNotExported() {
	t := s.T()
	tpl := workloads.EchoWithNestedVolume("agenthard-nested")
	tpl.Annotations = map[string]string{annotation.InjectIgnoreVolumeMounts: workloads.NestedVolumeK8sName}
	wl := s.Workload(tpl)

	a, sc, ftp, cn := s.agenthardAttachWithMount(wl)
	for _, p := range mountedPaths(a) {
		s.NotEqual(workloads.DataVolumeMountPath, p, "TELEPRESENCE_MOUNTS %q should not list the directory with an unshared mount below it", mountedPaths(a))
	}

	nested := path.Join(workloads.DataVolumeMountPath, "nested", workloads.NestedVolumeFileName)
	seed := path.Join(workloads.DataVolumeMountPath, workloads.DataVolumeSeedFile)
	for _, p := range []string{nested, seed} {
		content, err := sftpReadAll(sc, path.Join(agenthardExports, cn, p))
		s.Error(err, "SFTP read of %s should fail, got %q", p, content)
	}
	if _, err := sc.Stat(path.Join(agenthardExports, cn, workloads.DataVolumeMountPath)); err == nil {
		t.Errorf("SFTP stat of %s succeeded; the directory must not be exported", workloads.DataVolumeMountPath)
	}

	for _, p := range []string{nested, seed} {
		code, text := ftp.cmd("SIZE /%s", path.Join(cn, p))
		s.False(ftpSucceeded(code), "FTP SIZE of %s should fail, got %s", p, text)
	}
}

// Test_ReadOnlyVolumeNotOverFtp intercepts, over a node-agent with a mount, a workload with a
// writable volume that has the remote-read-only policy. Over SFTP the volume stays readable
// and refuses writes. The FTP server cannot enforce read-only, so it must not offer the volume:
// a directory creation and a delete in it must fail, and so must a size query of its file.
func (s *AgenthardSftp) Test_ReadOnlyVolumeNotOverFtp() {
	t := s.T()
	tpl := workloads.EchoWithDataVolume("agenthard-readonly")
	tpl.Annotations = map[string]string{
		annotation.VolumeMountPolicies: fmt.Sprintf(`{"%s":"RemoteReadOnly"}`, workloads.DataVolumeK8sName),
	}
	wl := s.Workload(tpl)

	a, sc, ftp, cn := s.agenthardAttachWithMount(wl)
	s.Contains(mountedPaths(a), workloads.DataVolumeMountPath, "TELEPRESENCE_MOUNTS should list the read-only volume")

	seed := path.Join(workloads.DataVolumeMountPath, workloads.DataVolumeSeedFile)
	content, err := sftpReadAll(sc, path.Join(agenthardExports, cn, seed))
	s.Require().NoError(err, "SFTP read of %s", seed)
	s.Equal(workloads.DataVolumeSeedContent, content)
	newFile := path.Join(agenthardExports, cn, workloads.DataVolumeMountPath, "agenthard-sftp-write")
	if f, err := sc.OpenFile(newFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL); err == nil {
		_ = f.Close()
		_ = sc.Remove(newFile)
		t.Errorf("SFTP created %s in a read-only volume", newFile)
	}

	dir := path.Join(cn, workloads.DataVolumeMountPath, fmt.Sprintf("agenthard-ftp-%d", time.Now().UnixNano()))
	code, text := ftp.cmd("MKD /%s", dir)
	if ftpSucceeded(code) {
		_, _ = ftp.cmd("RMD /%s", dir)
		t.Errorf("FTP created a directory in a read-only volume: %s", text)
	}
	code, text = ftp.cmd("DELE /%s", path.Join(cn, seed))
	s.False(ftpSucceeded(code), "FTP DELE in a read-only volume should fail, got %s", text)
	code, text = ftp.cmd("SIZE /%s", path.Join(cn, seed))
	s.False(ftpSucceeded(code), "FTP should not offer a read-only volume, SIZE got %s", text)

	content, err = sftpReadAll(sc, path.Join(agenthardExports, cn, seed))
	s.Require().NoError(err, "SFTP read of %s after the FTP attempts", seed)
	s.Equal(workloads.DataVolumeSeedContent, content, "the file must be unchanged")
}
