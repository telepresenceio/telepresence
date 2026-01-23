package integration_test

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/json"
)

func (s *notConnectedSuite) Test_CloudNeverProxy() {
	require := s.Require()
	ctx := s.Context()

	svcName := "echo-never-proxy"
	itest.ApplyEchoService(ctx, svcName, s.AppNamespace(), 8080)
	defer itest.DeleteSvcAndWorkload(ctx, "deploy", svcName, s.AppNamespace())

	ipStr, err := itest.Output(ctx, "kubectl",
		"--namespace", s.AppNamespace(),
		"get", "svc", svcName,
		"-o",
		"jsonpath={.spec.clusterIP}")
	require.NoError(err)
	ip, err := netip.ParseAddr(ipStr)
	require.NoError(err)
	if ip.IsLoopback() {
		s.T().Skipf("test can't run on host with a loopback cluster IP %s", ip)
	}

	mask := 32
	if s.IsIPv6() {
		mask = 128
	}

	kc := itest.KubeConfig(ctx)
	cfg, err := clientcmd.LoadFromFile(kc)
	require.NoError(err)
	ktx := cfg.Contexts[cfg.CurrentContext]
	require.NotNil(ktx, "unable to get current context from config")
	cluster := cfg.Clusters[ktx.Cluster]
	require.NotNil(cluster, "unable to get %s cluster from config", ktx.Cluster)
	ips, err := getClusterIPs(cluster)
	require.NoError(err)

	s.TelepresenceHelmInstallOK(ctx, true, "--set", fmt.Sprintf("client.routing.neverProxySubnets={%s/%d}", ip, mask))
	defer s.RollbackTM(ctx)

	timeout := 20 * time.Second
	if runtime.GOOS == "windows" {
		timeout *= 5
	}
	s.Eventuallyf(func() bool {
		defer func() {
			stdout, stderr, err := itest.Telepresence(ctx, "quit")
			clog.Infof(ctx, "stdout: %q", stdout)
			clog.Infof(ctx, "stderr: %q", stderr)
			if err != nil {
				clog.Error(ctx, err)
			}
		}()
		stdout, stderr, err := itest.Telepresence(ctx, "connect", "--namespace", s.AppNamespace(), "--manager-namespace", s.ManagerNamespace())
		clog.Infof(ctx, "stdout: %q", stdout)
		clog.Infof(ctx, "stderr: %q", stderr)
		if err != nil {
			clog.Error(ctx, err)
			return false
		}

		neverProxiedCount := 1

		// The cluster's IP address will be never proxied unless it's a loopback, so we gotta account for that.
		for _, cip := range ips {
			if !cip.IsLoopback() {
				neverProxiedCount++
			}
		}

		stdout, stderr, err = itest.Telepresence(ctx, "status")
		clog.Infof(ctx, "stdout: %q", stdout)
		clog.Infof(ctx, "stderr: %q", stderr)
		if err != nil {
			clog.Error(ctx, err)
			return false
		}
		m := regexp.MustCompile(`Never Proxy\s*:\s*\((\d+) subnets\)`).FindStringSubmatch(stdout)
		npcOk := false
		if m != nil {
			npc, _ := strconv.Atoi(m[1])
			npcOk = npc > 0 && npc <= neverProxiedCount
		}
		if !npcOk {
			clog.Errorf(ctx, "did not find 1-%d never-proxied subnets", neverProxiedCount)
			return false
		}

		view, err := s.configView()
		require.NoError(err)
		npc := len(view.Config.Routing().NeverProxy)
		npcOk = npc > 0 && npc <= neverProxiedCount
		if !npcOk {
			clog.Errorf(ctx, "did not find 1-%d never-proxied subnets in json status", neverProxiedCount)
			return false
		}

		if itest.Run(ctx, "curl", "--silent", "--max-time", "0.5", ip.String()) == nil {
			clog.Errorf(ctx, "never-proxied IP %s is reachable", ip)
			return false
		}

		clog.Infof(ctx, "Success! Never-proxied IP %s is not reachable", ip)
		return true
	}, timeout, 5*time.Second, "never-proxy not updated in %s", timeout)
}

func (s *notConnectedSuite) Test_CloudAllowConflicting() {
	require := s.Require()
	ctx := s.Context()

	acs, err := netip.ParsePrefix("10.88.2.4/30")
	require.NoError(err)
	s.TelepresenceHelmInstallOK(ctx, true, "--set", fmt.Sprintf("client.routing.allowConflictingSubnets={%s}", acs))
	defer s.RollbackTM(ctx)

	timeout := 20 * time.Second
	if runtime.GOOS == "windows" {
		timeout *= 5
	}
	s.Eventuallyf(func() bool {
		defer func() {
			stdout, stderr, err := itest.Telepresence(ctx, "quit")
			clog.Infof(ctx, "stdout: %q", stdout)
			clog.Infof(ctx, "stderr: %q", stderr)
			if err != nil {
				clog.Error(ctx, err)
			}
		}()
		s.TelepresenceConnect(ctx)
		sr, err := itest.TelepresenceStatus(ctx)
		if err != nil {
			return false
		}
		ac := sr.RootDaemon.AllowConflicting
		if len(ac) != 1 {
			return false
		}
		return len(ac) == 1 && netip.MustParsePrefix(ac[0].String()) == acs
	}, timeout, 5*time.Second, "allow-conflicting-subnets not updated in %s", timeout)
}

func (s *notConnectedSuite) configView() (*client.SessionConfig, error) {
	stdout, _, err := itest.Telepresence(s.Context(), "config", "view", "--output", "json")
	if err != nil {
		return nil, err
	}
	var view client.SessionConfig
	err = json.Unmarshal([]byte(stdout), &view, false)
	return &view, err
}

func (s *notConnectedSuite) Test_CloudAgentArrival() {
	ctx := s.Context()
	const agentArrivalTimeout = 2 * time.Minute
	s.TelepresenceHelmInstallOK(ctx, true, "--set", fmt.Sprintf("timeouts.agentArrival=%s", agentArrivalTimeout))
	defer s.RollbackTM(ctx)

	timeout := 20 * time.Second
	if runtime.GOOS == "windows" {
		timeout *= 5
	}

	s.Eventuallyf(func() bool {
		defer func() {
			stdout, stderr, err := itest.Telepresence(ctx, "quit")
			clog.Infof(ctx, "stdout: %q", stdout)
			clog.Infof(ctx, "stderr: %q", stderr)
			if err != nil {
				clog.Error(ctx, err)
			}
		}()
		s.TelepresenceConnect(ctx)
		view, err := s.configView()
		if err != nil {
			return false
		}
		return view.Timeouts().Get(client.TimeoutIntercept) == agentArrivalTimeout
	}, timeout, 5*time.Second, "timeouts.intercept not updated by changing traffic-manager's timeouts.agentArrival in %s", timeout)
}

func (s *notConnectedSuite) Test_RootdCloudLogLevel() {
	require := s.Require()
	ctx := s.Context()

	rootLogName := filepath.Join(filelocation.AppUserLogDir(ctx), "daemon.log")
	// Figure out where the current end of the logfile is.
	pos := int64(0)
	st, err := os.Stat(rootLogName)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.T().Fatalf("Unexpected error stat'ing %s: %v", rootLogName, err)
		}
	} else {
		pos = st.Size()
	}

	s.TelepresenceHelmInstallOK(ctx, true, "--set", "logLevel=debug,agent.logLevel=debug,client.logLevels.rootDaemon=trace")
	defer s.RollbackTM(ctx)

	ctx = itest.WithConfig(ctx, func(cfg client.Config) {
		cfg.LogLevels().RootDaemon = slog.LevelInfo
	})

	s.Eventually(func() bool {
		_, _, err = itest.Telepresence(ctx, "connect", "--namespace", s.AppNamespace(), "--manager-namespace", s.ManagerNamespace())
		if err != nil {
			return false
		}
		itest.TelepresenceDisconnectOk(ctx)

		rootLog, err := os.Open(rootLogName)
		require.NoError(err)
		defer rootLog.Close()
		_, err = rootLog.Seek(pos, 0)
		require.NoError(err)

		levelSet := false
		scn := bufio.NewScanner(rootLog)
		for scn.Scan() && !levelSet {
			line := scn.Text()
			levelSet = strings.Contains(line, `Logging at this level "TRACE"`)
			pos += int64(len(line)) + 1
		}
		return levelSet
	}, 60*time.Second, 5*time.Second, "Root log level not updated in 20 seconds")

	// Make sure the log level was set back after disconnect
	s.Eventually(func() bool {
		rootLog, err := os.Open(rootLogName)
		require.NoError(err)
		defer rootLog.Close()
		_, err = rootLog.Seek(pos, 0)
		require.NoError(err)

		levelSet := false
		scn := bufio.NewScanner(rootLog)
		for scn.Scan() && !levelSet {
			line := scn.Text()
			levelSet = strings.Contains(line, `Logging at this level "INFO"`)
			pos += int64(len(line)) + 1
		}
		return levelSet
	}, 5*time.Second, time.Second, "Root log level not reset after disconnect")

	// Set it to a "real" value to see that the client-side wins
	ctx = itest.WithConfig(ctx, func(config client.Config) {
		config.LogLevels().RootDaemon = slog.LevelDebug
	})

	s.TelepresenceConnect(ctx)
	rootLog, err := os.Open(rootLogName)
	require.NoError(err)
	defer rootLog.Close()
	_, err = rootLog.Seek(pos, 0)
	require.NoError(err)

	levelSet := false
	scn := bufio.NewScanner(rootLog)
	for scn.Scan() && !levelSet {
		levelSet = strings.Contains(scn.Text(), `Logging at this level "TRACE"`)
	}
	itest.TelepresenceDisconnectOk(ctx)
	require.False(levelSet, "Root log level not respected when set in config file")
	view, err := s.configView()
	require.NoError(err)
	require.Equal(view.LogLevels().RootDaemon, slog.LevelDebug)
}

func (s *notConnectedSuite) Test_UserdCloudLogLevel() {
	require := s.Require()
	ctx := s.Context()

	userLogName := filepath.Join(filelocation.AppUserLogDir(ctx), "connector.log")
	// Figure out where the current end of the logfile is.
	pos := int64(0)
	st, err := os.Stat(userLogName)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.T().Fatalf("Unexpected error stat'ing %s: %v", userLogName, err)
		}
	} else {
		pos = st.Size()
	}

	s.TelepresenceHelmInstallOK(ctx, true, "--set", "logLevel=debug,agent.logLevel=debug,client.logLevels.userDaemon=trace")
	defer s.RollbackTM(ctx)
	ctx = itest.WithConfig(ctx, func(cfg client.Config) {
		cfg.LogLevels().UserDaemon = slog.LevelInfo
	})

	s.Eventually(func() bool {
		so, se, err := itest.Telepresence(ctx, "connect", "--manager-namespace", s.ManagerNamespace(), "--namespace", s.AppNamespace())
		clog.Infof(ctx, "stdout %s", so)
		clog.Infof(ctx, "stderr %s", se)
		if err != nil {
			clog.Error(ctx, err)
			return false
		}
		itest.TelepresenceDisconnectOk(ctx)

		logF, err := os.Open(userLogName)
		require.NoError(err)
		defer logF.Close()
		_, err = logF.Seek(pos, 0)
		require.NoError(err)

		scn := bufio.NewScanner(logF)
		levelSet := false
		for scn.Scan() && !levelSet {
			line := scn.Text()
			levelSet = strings.Contains(line, `Logging at this level "TRACE"`)
			pos += int64(len(line)) + 1
		}
		return levelSet
	}, 20*time.Second, 5*time.Second, "Connector log level not updated in 20 seconds")

	// Make sure the log level was set back after disconnect
	logF, err := os.Open(userLogName)
	require.NoError(err)
	_, err = logF.Seek(pos, 0)
	if !s.NoError(err) {
		logF.Close()
		return
	}

	scn := bufio.NewScanner(logF)
	levelSet := false
	for scn.Scan() && !levelSet {
		levelSet = strings.Contains(scn.Text(), `Logging at this level "INFO"`)
	}
	logF.Close()
	require.True(levelSet, "Connector log level not reset after disconnect")

	// Set it to a "real" value to see that the client-side wins
	ctx = itest.WithConfig(ctx, func(config client.Config) {
		config.LogLevels().UserDaemon = slog.LevelDebug
	})

	s.TelepresenceConnect(ctx)
	itest.TelepresenceDisconnectOk(ctx)

	logF, err = os.Open(userLogName)
	require.NoError(err)
	_, err = logF.Seek(pos, 0)
	if !s.NoError(err) {
		logF.Close()
		return
	}

	levelSet = false
	for scn.Scan() && !levelSet {
		levelSet = strings.Contains(scn.Text(), `Logging at this level "TRACE"`)
	}
	logF.Close()
	require.False(levelSet, "Connector log level not respected when set in config file")
}
