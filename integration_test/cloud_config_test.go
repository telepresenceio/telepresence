package integration_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func (s *notConnectedSuite) Test_CloudNeverProxy() {
	require := s.Require()
	ctx := s.Context()
	itest.TelepresenceQuitOk(ctx)

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

	tmpdir := s.T().TempDir()
	values := path.Join(tmpdir, "values.yaml")
	f, err := os.Create(values)
	require.NoError(err)
	b, err := yaml.Marshal(
		map[string]map[string][]string{
			"client": {
				"never-proxy": {fmt.Sprintf("%s/32", ip)},
			},
		},
	)
	require.NoError(err)
	_, err = f.Write(b)
	require.NoError(err)

	itest.TelepresenceOk(ctx, "helm", "install", "--upgrade", "--set", "logLevel=debug,agent.logLevel=debug", "-f", values)
	defer func() {
		require.NoError(itest.Run(ctx, "helm", "rollback", "--namespace", s.ManagerNamespace(), "traffic-manager"))
	}()

	s.Eventually(func() bool {
		itest.TelepresenceOk(ctx, "connect")
		defer itest.TelepresenceQuitOk(ctx)

		// The cluster's IP address will also be never proxied, so we gotta account for that.
		neverProxiedCount := len(ips) + 1
		stdout := itest.TelepresenceOk(ctx, "status")
		if !strings.Contains(stdout, fmt.Sprintf("Never Proxy: (%d subnets)", neverProxiedCount)) {
			dlog.Errorf(ctx, "did not find %d never-proxied subnets", neverProxiedCount)
			return false
		}

		jsonStdout := itest.TelepresenceOk(ctx, "status", "--json")
		var status statusResponse
		require.NoError(json.Unmarshal([]byte(jsonStdout), &status))
		if len(status.RootDaemon.NeverProxySubnets) != neverProxiedCount {
			dlog.Errorf(ctx, "did not find %d never-proxied subnets in json status", neverProxiedCount)
			return false
		}

		if itest.Run(ctx, "curl", "--silent", "--max-time", "0.5", ip) == nil {
			dlog.Errorf(ctx, "never-proxied IP %s is reachable", ip)
			return false
		}

		return true
	}, 125*time.Second, 5*time.Second, "never-proxy not updated in 2 minutes")
}

func (s *notConnectedSuite) Test_RootdCloudLogLevel() {
	require := s.Require()
	ctx := s.Context()
	itest.TelepresenceQuitOk(ctx)

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

	tmpdir := s.T().TempDir()
	values := path.Join(tmpdir, "values.yaml")
	f, err := os.Create(values)
	require.NoError(err)
	b, err := yaml.Marshal(
		map[string]map[string]map[string]string{
			"client": {
				"logLevels": {
					"rootDaemon": "trace",
				},
			},
		},
	)
	require.NoError(err)
	_, err = f.Write(b)
	require.NoError(err)
	require.NoError(f.Close())

	itest.TelepresenceOk(ctx, "helm", "install", "--upgrade", "--set", "logLevel=debug,agent.logLevel=debug", "-f", values)
	defer func() {
		require.NoError(itest.Run(ctx, "helm", "rollback", "--namespace", s.ManagerNamespace(), "traffic-manager"))
	}()

	// logrus.InfoLevel is the 0 value, so it's considered as unset if that's what's used,
	// and the traffic-manager will be able to apply its own. For this same reason we can't use WithConfig to do this,
	// as it'll not merge the info config into the existing one.
	origConfig := client.GetConfig(ctx)
	config := *origConfig // copy
	config.LogLevels.RootDaemon = logrus.InfoLevel
	configYaml, err := yaml.Marshal(&config)
	require.NoError(err)
	configYamlStr := string(configYaml)
	configDir := s.T().TempDir()
	ctx = filelocation.WithAppUserConfigDir(ctx, configDir)
	ctx, err = client.SetConfig(ctx, configDir, configYamlStr)
	require.NoError(err)

	var currentLine int64
	s.Eventually(func() bool {
		itest.TelepresenceOk(ctx, "connect")
		defer itest.TelepresenceQuitOk(ctx)

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
	}, 125*time.Second, 5*time.Second, "Root log level not updated in 2 minutes")

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
	ctx = itest.WithConfig(ctx, &client.Config{
		LogLevels: client.LogLevels{
			RootDaemon: logrus.DebugLevel,
		},
	})
	itest.TelepresenceOk(ctx, "connect")
	defer itest.TelepresenceQuitOk(ctx)
	levelSet = false
	for scn.Scan() && !levelSet {
		levelSet = strings.Contains(scn.Text(), `Logging at this level "trace"`)
	}
	require.False(levelSet, "Root log level not respected when set in config file")
}

func (s *notConnectedSuite) Test_UserdCloudLogLevel() {
	require := s.Require()
	ctx := s.Context()
	itest.TelepresenceQuitOk(ctx)

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

	tmpdir := s.T().TempDir()
	values := path.Join(tmpdir, "values.yaml")
	f, err := os.Create(values)
	require.NoError(err)
	b, err := yaml.Marshal(
		map[string]map[string]map[string]string{
			"client": {
				"logLevels": {
					"userDaemon": "trace",
				},
			},
		},
	)
	require.NoError(err)
	_, err = f.Write(b)
	require.NoError(err)
	require.NoError(f.Close())

	itest.TelepresenceOk(ctx, "helm", "install", "--upgrade", "--set", "logLevel=debug,agent.logLevel=debug", "-f", values)
	defer func() {
		require.NoError(itest.Run(ctx, "helm", "rollback", "--namespace", s.ManagerNamespace(), "traffic-manager"))
	}()

	// logrus.InfoLevel is the 0 value, so it's considered as unset if that's what's used,
	// and the traffic-manager will be able to apply its own. For this same reason we can't use WithConfig to do this,
	// as it'll not merge the info config into the existing one.
	origConfig := client.GetConfig(ctx)
	config := *origConfig // copy
	config.LogLevels.UserDaemon = logrus.InfoLevel
	configYaml, err := yaml.Marshal(&config)
	require.NoError(err)
	configYamlStr := string(configYaml)
	configDir := s.T().TempDir()
	ctx = filelocation.WithAppUserConfigDir(ctx, configDir)
	ctx, err = client.SetConfig(ctx, configDir, configYamlStr)
	require.NoError(err)

	var currentLine int64
	s.Eventually(func() bool {
		itest.TelepresenceOk(ctx, "connect")
		defer itest.TelepresenceQuitOk(ctx)

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
	}, 125*time.Second, 5*time.Second, "Connector log level not updated in 2 minutes")

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
	ctx = itest.WithConfig(ctx, &client.Config{
		LogLevels: client.LogLevels{
			UserDaemon: logrus.DebugLevel,
		},
	})
	itest.TelepresenceOk(ctx, "connect")
	defer itest.TelepresenceQuitOk(ctx)
	levelSet = false
	for scn.Scan() && !levelSet {
		levelSet = strings.Contains(scn.Text(), `Logging at this level "trace"`)
	}
	require.False(levelSet, "Connector log level not respected when set in config file")
}
