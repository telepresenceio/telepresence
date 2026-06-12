package integration_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

// localShortcutSuite verifies that the root daemon connects outbound traffic to
// destinations covered by the client's own intercepts directly to the local intercept
// handler instead of tunneling to the cluster (issue #4125).
type localShortcutSuite struct {
	itest.Suite
	itest.TrafficManager
	svc string
}

func (s *localShortcutSuite) SuiteName() string {
	return "LocalShortcut"
}

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &localShortcutSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h, svc: "echo-shortcut"}
	})
}

func (s *localShortcutSuite) SetupSuite() {
	s.Suite.SetupSuite()
	itest.ApplyEchoService(s.Context(), s.svc, s.AppNamespace(), 80)
}

func (s *localShortcutSuite) TearDownSuite() {
	s.DeleteSvcAndWorkload(s.Context(), "deploy", s.svc)
}

const shortcutHeader = "x-shortcut-test"

// interceptAndDial connects, intercepts the suite's workload with a local echo
// handler, dials the service by its cluster DNS name from the workstation, and
// returns the part of daemon.log that was appended while doing so. When
// headerFilter is true, the intercept is created with an HTTP header filter and
// the dials carry a matching header.
func (s *localShortcutSuite) interceptAndDial(ctx context.Context, headerFilter bool) string {
	rq := s.Require()

	logFile := filepath.Join(filelocation.AppUserLogDir(ctx), "daemon.log")
	logSize := int64(0)
	if st, err := os.Stat(logFile); err == nil {
		logSize = st.Size()
	}

	localPort, cancel := startLocalH2CEchoServer(ctx, s.svc)
	defer cancel()

	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceQuitOk(ctx)
	iceptArgs := []string{"intercept", s.svc, "--port", strconv.Itoa(localPort) + ":80", "--mount=false"}
	var curlArgs []string
	if headerFilter {
		iceptArgs = append(iceptArgs, "--http-header", shortcutHeader+"=yes")
		curlArgs = []string{"-H", shortcutHeader + ": yes"}
	}
	itest.TelepresenceOk(ctx, iceptArgs...)
	defer func() {
		_, _, _ = itest.Telepresence(ctx, "leave", s.svc)
	}()
	rq.NoError(itest.RolloutStatusWait(ctx, s.AppNamespace(), "deploy/"+s.svc))

	curl := func(host string, hdrs []string) (string, error) {
		args := append([]string{"--silent", "--max-time", "2"}, hdrs...)
		return itest.Output(ctx, "curl", append(args, host)...)
	}

	// The response must be served by the local intercept handler.
	host := s.svc + "." + s.AppNamespace()
	rq.Eventually(func() bool {
		out, err := curl(host, curlArgs)
		return err == nil && strings.Contains(out, s.svc+" from intercept")
	}, 30*time.Second, 2*time.Second, "no response from the local intercept handler")

	// Dial the intercepted pod directly too, so that the workload-based pod-IP
	// matching is exercised alongside the service address.
	podIP, err := itest.KubectlOut(ctx, s.AppNamespace(), "get", "pods", "-l", "app="+s.svc,
		"-o", "jsonpath={.items[0].status.podIP}")
	rq.NoError(err)
	rq.Eventually(func() bool {
		out, err := curl(net.JoinHostPort(podIP, "8080"), curlArgs)
		return err == nil && strings.Contains(out, s.svc+" from intercept")
	}, 30*time.Second, 2*time.Second, "no response from the local intercept handler via pod IP")

	logF, err := os.Open(logFile)
	rq.NoError(err)
	defer logF.Close()
	_, err = logF.Seek(logSize, 0)
	rq.NoError(err)
	bf := strings.Builder{}
	sc := bufio.NewScanner(logF)
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "Shortcutting") {
			fmt.Fprintln(&bf, line)
		}
	}
	return bf.String()
}

// Test_ShortcutActiveTraffic verifies that connections to the intercepted service,
// both via its service address and via its pod IP, are piped directly to the local
// intercept handler.
func (s *localShortcutSuite) Test_ShortcutActiveTraffic() {
	ctx := itest.WithConfig(s.Context(), func(cfg client.Config) {
		cfg.Intercept().LocalShortcut = true
	})
	shortcutLines := s.interceptAndDial(ctx, false)
	rq := s.Require()
	rq.Contains(shortcutLines, ":80 to local intercept handler", "no shortcut for the service address")
	rq.Contains(shortcutLines, ":8080 to local intercept handler", "no shortcut for the pod address")
}

// Test_ShortcutDisabled verifies that intercepted destinations take the ordinary
// round trip through the cluster when intercept.localShortcut is disabled, while
// traffic still arrives at the local intercept handler.
func (s *localShortcutSuite) Test_ShortcutDisabled() {
	ctx := itest.WithConfig(s.Context(), func(cfg client.Config) {
		cfg.Intercept().LocalShortcut = false
	})
	shortcutLines := s.interceptAndDial(ctx, false)
	s.Require().Empty(shortcutLines, "shortcuts must not be used when intercept.localShortcut is false")
}

// Test_ShortcutNotGlobal verifies that a header-filtered intercept is exempted from
// the shortcut when intercept.localShortcutIsGlobal is disabled. Requests carrying a
// matching header still arrive at the local intercept handler, but via the round
// trip through the cluster where the filter is evaluated.
func (s *localShortcutSuite) Test_ShortcutNotGlobal() {
	ctx := itest.WithConfig(s.Context(), func(cfg client.Config) {
		cfg.Intercept().LocalShortcut = true
		cfg.Intercept().LocalShortcutIsGlobal = false
	})
	shortcutLines := s.interceptAndDial(ctx, true)
	s.Require().Empty(shortcutLines, "a filtered intercept must not be shortcut when intercept.localShortcutIsGlobal is false")
}
