package integration_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func (s *notConnectedSuite) Test_CloudNeverProxy() {
	require := s.Require()
	ctx := s.Context()

	svcName := "echo-never-proxy"
	itest.ApplyEchoService(ctx, svcName, s.AppNamespace(), 8080)
	defer itest.DeleteSvcAndWorkload(ctx, "deploy", svcName, s.AppNamespace())

	ip, err := itest.Output(ctx, "kubectl",
		"--namespace", s.AppNamespace(),
		"get", "svc", svcName,
		"-o",
		"jsonpath={.spec.clusterIP}")
	require.NoError(err)

	kc := itest.KubeConfig(ctx)
	cfg, err := clientcmd.LoadFromFile(kc)
	require.NoError(err)
	cluster := cfg.Clusters["default"]
	require.NotNil(s.T(), cluster, "unable to get default cluster from config")
	ips, err := getClusterIPs(cluster)
	require.NoError(err)

	require.NoError(s.TelepresenceHelmInstall(ctx, true, "--set", fmt.Sprintf("client.routing.neverProxySubnets={%s/32}", ip)))
	defer s.RollbackTM(ctx)

	s.Eventually(func() bool {
		defer itest.TelepresenceDisconnectOk(ctx)
		itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace())

		// The cluster's IP address will also be never proxied, so we gotta account for that.
		neverProxiedCount := len(ips) + 1
		stdout := itest.TelepresenceOk(ctx, "status")
		if !strings.Contains(stdout, fmt.Sprintf("Never Proxy: (%d subnets)", neverProxiedCount)) {
			dlog.Errorf(ctx, "did not find %d never-proxied subnets", neverProxiedCount)
			return false
		}

		jsonStdout := itest.TelepresenceOk(ctx, "config", "view", "--output", "json")
		var view client.SessionConfig
		require.NoError(json.Unmarshal([]byte(jsonStdout), &view))
		if len(view.Routing.NeverProxy) != neverProxiedCount {
			dlog.Errorf(ctx, "did not find %d never-proxied subnets in json status", neverProxiedCount)
			return false
		}

		if itest.Run(ctx, "curl", "--silent", "--max-time", "0.5", ip) == nil {
			dlog.Errorf(ctx, "never-proxied IP %s is reachable", ip)
			return false
		}

		return true
	}, 20*time.Second, 5*time.Second, "never-proxy not updated in 20 seconds")
}

func (s *notConnectedSuite) Test_RootdCloudLogLevel() {
	require := s.Require()
	ctx := s.Context()

	// The log file may have junk from other tests in it, so we'll do a very simple method
	// of rushing to the end of the file and remembering where we left off when we start looking
	// for new lines.
	var lines int64
	logDir, err := filelocation.AppUserLogDir(ctx)
	require.NoError(err)
	rootLogName := filepath.Join(logDir, "daemon.log")
	rootLog, err := os.Open(rootLogName)
	require.NoError(err)
	scn := bufio.NewScanner(rootLog)
	for scn.Scan() {
		lines++
	}
	rootLog.Close()
	require.NoError(s.TelepresenceHelmInstall(ctx, true, "--set", "logLevel=debug,agent.logLevel=debug,client.logLevels.rootDaemon=trace"))
	defer s.RollbackTM(ctx)

	ctx = itest.WithConfig(ctx, func(cfg *client.Config) {
		cfg.LogLevels.RootDaemon = logrus.InfoLevel
	})

	var currentLine int64
	s.Eventually(func() bool {
		itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace())
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
	}, 20*time.Second, 5*time.Second, "Root log level not updated in 20 seconds")

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
	ctx = itest.WithConfig(ctx, func(config *client.Config) {
		config.LogLevels.RootDaemon = logrus.DebugLevel
	})
	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace())
	itest.TelepresenceDisconnectOk(ctx)
	levelSet = false
	for scn.Scan() && !levelSet {
		levelSet = strings.Contains(scn.Text(), `Logging at this level "trace"`)
	}
	require.False(levelSet, "Root log level not respected when set in config file")

	var view client.SessionConfig
	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace())
	jsonStdout := itest.TelepresenceOk(ctx, "config", "view", "--output", "json")
	require.NoError(json.Unmarshal([]byte(jsonStdout), &view))
	require.Equal(view.LogLevels.RootDaemon, logrus.DebugLevel)
}

func (s *notConnectedSuite) Test_UserdCloudLogLevel() {
	require := s.Require()
	ctx := s.Context()

	// The log file may have junk from other tests in it, so we'll do a very simple method
	// of rushing to the end of the file and remembering where we left off when we start looking
	// for new lines.
	var lines int64
	logDir, err := filelocation.AppUserLogDir(ctx)
	require.NoError(err)
	logName := filepath.Join(logDir, "connector.log")
	logF, err := os.Open(logName)
	require.NoError(err)
	scn := bufio.NewScanner(logF)
	for scn.Scan() {
		lines++
	}
	logF.Close()

	require.NoError(s.TelepresenceHelmInstall(ctx, true, "--set", "logLevel=debug,agent.logLevel=debug,client.logLevels.userDaemon=trace"))
	defer s.RollbackTM(ctx)
	ctx = itest.WithConfig(ctx, func(cfg *client.Config) {
		cfg.LogLevels.UserDaemon = logrus.InfoLevel
	})

	var currentLine int64
	s.Eventually(func() bool {
		itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace())
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
	}, 20*time.Second, 5*time.Second, "Connector log level not updated in 20 seconds")

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
	ctx = itest.WithConfig(ctx, func(config *client.Config) {
		config.LogLevels.UserDaemon = logrus.DebugLevel
	})

	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace())
	itest.TelepresenceDisconnectOk(ctx)

	levelSet = false
	for scn.Scan() && !levelSet {
		levelSet = strings.Contains(scn.Text(), `Logging at this level "trace"`)
	}
	require.False(levelSet, "Connector log level not respected when set in config file")
}
