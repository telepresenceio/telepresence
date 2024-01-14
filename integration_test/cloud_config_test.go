package integration_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
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
	ip := iputil.Parse(ipStr)
	require.NotNil(ip)
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

	require.NoError(s.TelepresenceHelmInstall(ctx, true, "--set", fmt.Sprintf("client.routing.neverProxySubnets={%s/%d}", ip, mask)))
	defer s.RollbackTM(ctx)

	timeout := 20 * time.Second
	if runtime.GOOS == "windows" {
		timeout *= 5
	}
	s.Eventuallyf(func() bool {
		defer func() {
			stdout, stderr, err := itest.Telepresence(ctx, "quit")
			dlog.Infof(ctx, "stdout: %q", stdout)
			dlog.Infof(ctx, "stderr: %q", stderr)
			if err != nil {
				dlog.Error(ctx, err)
			}
		}()
		stdout, stderr, err := itest.Telepresence(ctx, "connect", "--namespace", s.AppNamespace(), "--manager-namespace", s.ManagerNamespace())
		dlog.Infof(ctx, "stdout: %q", stdout)
		dlog.Infof(ctx, "stderr: %q", stderr)
		if err != nil {
			dlog.Error(ctx, err)
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
		dlog.Infof(ctx, "stdout: %q", stdout)
		dlog.Infof(ctx, "stderr: %q", stderr)
		if err != nil {
			dlog.Error(ctx, err)
			return false
		}
		m := regexp.MustCompile(`Never Proxy\s*:\s*\((\d+) subnets\)`).FindStringSubmatch(stdout)
		if m == nil || m[1] != strconv.Itoa(neverProxiedCount) {
			dlog.Errorf(ctx, "did not find %d never-proxied subnets", neverProxiedCount)
			return false
		}

		jsonStdout, _, err := itest.Telepresence(ctx, "config", "view", "--output", "json")
		if err != nil {
			dlog.Error(ctx, err)
			return false
		}
		var view client.SessionConfig
		require.NoError(json.Unmarshal([]byte(jsonStdout), &view))
		if len(view.Routing.NeverProxy) != neverProxiedCount {
			dlog.Errorf(ctx, "did not find %d never-proxied subnets in json status", neverProxiedCount)
			return false
		}

		if itest.Run(ctx, "curl", "--silent", "--max-time", "0.5", ip.String()) == nil {
			dlog.Errorf(ctx, "never-proxied IP %s is reachable", ip)
			return false
		}

		dlog.Infof(ctx, "Success! Never-proxied IP %s is not reachable", ip)
		return true
	}, timeout, 5*time.Second, "never-proxy not updated in %s", timeout)
}

func (s *notConnectedSuite) Test_CloudAllowConflicting() {
	require := s.Require()
	ctx := s.Context()

	acs, err := netip.ParsePrefix("10.88.2.4/30")
	require.NoError(err)
	require.NoError(s.TelepresenceHelmInstall(ctx, true, "--set", fmt.Sprintf("client.routing.allowConflictingSubnets={%s}", acs)))
	defer s.RollbackTM(ctx)

	timeout := 20 * time.Second
	if runtime.GOOS == "windows" {
		timeout *= 5
	}
	s.Eventuallyf(func() bool {
		defer func() {
			stdout, stderr, err := itest.Telepresence(ctx, "quit")
			dlog.Infof(ctx, "stdout: %q", stdout)
			dlog.Infof(ctx, "stderr: %q", stderr)
			if err != nil {
				dlog.Error(ctx, err)
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

func (s *notConnectedSuite) Test_RootdCloudLogLevel() {
	require := s.Require()
	ctx := s.Context()

	// The log file may have junk from other tests in it, so we'll do a very simple method
	// of rushing to the end of the file and remembering where we left off when we start looking
	// for new lines.
	var lines int64
	rootLogName := filepath.Join(filelocation.AppUserLogDir(ctx), "daemon.log")
	rootLog, err := os.Open(rootLogName)
	require.NoError(err)
	scn := bufio.NewScanner(rootLog)
	for scn.Scan() {
		lines++
	}
	rootLog.Close()
	require.NoError(s.TelepresenceHelmInstall(ctx, true, "--set", "logLevel=debug,agent.logLevel=debug,client.logLevels.rootDaemon=trace"))
	defer s.RollbackTM(ctx)

	ctx = itest.WithConfig(ctx, func(cfg client.Config) {
		cfg.LogLevels().RootDaemon = logrus.InfoLevel
	})

	var currentLine int64
	s.Eventually(func() bool {
		_, _, err = itest.Telepresence(ctx, "connect", "--namespace", s.AppNamespace(), "--manager-namespace", s.ManagerNamespace())
		if err != nil {
			return false
		}
		itest.TelepresenceDisconnectOk(ctx)

		rootLog, err := os.Open(rootLogName)
		require.NoError(err)
		defer rootLog.Close()
		scn := bufio.NewScanner(rootLog)

		currentLine = 0
		for scn.Scan() && currentLine <= lines {
			currentLine++
		}

		levelSet := false
		for scn.Scan() && !levelSet {
			levelSet = strings.Contains(scn.Text(), `Logging at this level "trace"`)
			currentLine++
		}
		return levelSet
	}, 60*time.Second, 5*time.Second, "Root log level not updated in 20 seconds")

	// Make sure the log level was set back after disconnect
	rootLog, err = os.Open(rootLogName)
	require.NoError(err)
	defer rootLog.Close()
	scn = bufio.NewScanner(rootLog)

	lines = currentLine
	currentLine = 0
	for scn.Scan() && currentLine <= lines {
		currentLine++
	}

	levelSet := false
	for scn.Scan() && !levelSet {
		levelSet = strings.Contains(scn.Text(), `Logging at this level "info"`)
	}
	require.True(levelSet, "Root log level not reset after disconnect")

	// Set it to a "real" value to see that the client-side wins
	ctx = itest.WithConfig(ctx, func(config client.Config) {
		config.LogLevels().RootDaemon = logrus.DebugLevel
	})
	s.TelepresenceConnect(ctx)
	itest.TelepresenceDisconnectOk(ctx)
	levelSet = false
	for scn.Scan() && !levelSet {
		levelSet = strings.Contains(scn.Text(), `Logging at this level "trace"`)
	}
	require.False(levelSet, "Root log level not respected when set in config file")

	var view client.SessionConfig
	s.TelepresenceConnect(ctx)
	jsonStdout := itest.TelepresenceOk(ctx, "config", "view", "--output", "json")
	require.NoError(json.Unmarshal([]byte(jsonStdout), &view))
	require.Equal(view.LogLevels().RootDaemon, logrus.DebugLevel)
}

func (s *notConnectedSuite) Test_UserdCloudLogLevel() {
	require := s.Require()
	ctx := s.Context()

	// The log file may have junk from other tests in it, so we'll do a very simple method
	// of rushing to the end of the file and remembering where we left off when we start looking
	// for new lines.
	var lines int64
	logName := filepath.Join(filelocation.AppUserLogDir(ctx), "connector.log")
	logF, err := os.Open(logName)
	require.NoError(err)
	scn := bufio.NewScanner(logF)
	for scn.Scan() {
		lines++
	}
	logF.Close()

	require.NoError(s.TelepresenceHelmInstall(ctx, true, "--set", "logLevel=debug,agent.logLevel=debug,client.logLevels.userDaemon=trace"))
	defer s.RollbackTM(ctx)
	ctx = itest.WithConfig(ctx, func(cfg client.Config) {
		cfg.LogLevels().UserDaemon = logrus.InfoLevel
	})

	var currentLine int64
	s.Eventually(func() bool {
		_, _, err := itest.Telepresence(ctx, "connect", "--manager-namespace", s.ManagerNamespace())
		if err != nil {
			return false
		}
		itest.TelepresenceDisconnectOk(ctx)

		logF, err := os.Open(logName)
		require.NoError(err)
		defer logF.Close()
		scn := bufio.NewScanner(logF)

		currentLine = 0
		for scn.Scan() && currentLine <= lines {
			currentLine++
		}

		levelSet := false
		for scn.Scan() && !levelSet {
			levelSet = strings.Contains(scn.Text(), `Logging at this level "trace"`)
			currentLine++
		}
		return levelSet
	}, 60*time.Second, 5*time.Second, "Connector log level not updated in 20 seconds")

	// Make sure the log level was set back after disconnect
	logF, err = os.Open(logName)
	require.NoError(err)
	defer logF.Close()
	scn = bufio.NewScanner(logF)

	lines = currentLine
	currentLine = 0
	for scn.Scan() && currentLine <= lines {
		currentLine++
	}

	levelSet := false
	for scn.Scan() && !levelSet {
		levelSet = strings.Contains(scn.Text(), `Logging at this level "info"`)
	}
	require.True(levelSet, "Connector log level not reset after disconnect")

	// Set it to a "real" value to see that the client-side wins
	ctx = itest.WithConfig(ctx, func(config client.Config) {
		config.LogLevels().UserDaemon = logrus.DebugLevel
	})

	s.TelepresenceConnect(ctx)
	itest.TelepresenceDisconnectOk(ctx)

	levelSet = false
	for scn.Scan() && !levelSet {
		levelSet = strings.Contains(scn.Text(), `Logging at this level "trace"`)
	}
	require.False(levelSet, "Connector log level not respected when set in config file")
}
