package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	core "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dcontext"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
)

func (s *connectedSuite) Test_InterceptsContainer() {
	ctx, cancel := context.WithCancel(dcontext.WithSoftness(s.Context()))
	defer cancel()
	const svc = "echo-secondary"

	svcPort, svcCancel := itest.StartLocalHttpEchoServer(ctx, svc)
	defer svcCancel()

	s.ApplyApp(ctx, svc, "deploy/"+svc)
	defer s.DeleteSvcAndWorkload(ctx, "deployment", svc)
	defer func() {
		s.NoError(s.Kubectl(ctx, "delete", "configmaps", "socat-data", "echo-data"))
	}()

	require := s.Require()
	dir := s.T().TempDir()
	envFile := filepath.Join(dir, "env.json")
	stdout := itest.TelepresenceOk(ctx, "intercept", svc,
		"--output", "json",
		"--detailed-output",
		"--container", "echo",
		"--env-json", envFile,
		"--port", strconv.Itoa(svcPort))
	defer itest.TelepresenceOk(ctx, "leave", svc)

	var iInfo intercept.Info
	require.NoError(json.Unmarshal([]byte(stdout), &iInfo))
	s.CapturePodLogs(ctx, svc, "traffic-agent", s.AppNamespace())

	require.Eventually(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
			return err == nil && strings.Contains(stdout, svc+": intercepted")
		},
		30*time.Second, // waitFor
		3*time.Second,  // polling interval
		`intercepted workload never show up in list`)

	itest.PingInterceptedEchoServer(ctx, svc, "80")

	// Check that the env stems from the targeted container
	s.Equal(iInfo.Environment["TAG"], "echo-server")
	mountPoint := iInfo.Mount.LocalDir
	dataDir := filepath.Join(mountPoint, "usr", "share", "data")
	st, err := os.Stat(dataDir)
	require.NoError(err, "mount of %s should be successful", dataDir)
	require.True(st.IsDir())
	dataFile := filepath.Join(dataDir, "text")
	content, err := os.ReadFile(dataFile)
	require.NoError(err, "unable to read", dataFile)
	s.Equal("Hello from echo\n", string(content))

	// Intercept again, this time without the --container flag
	itest.TelepresenceOk(ctx, "leave", svc)
	stdout = itest.TelepresenceOk(ctx, "intercept", svc,
		"--output", "json",
		"--detailed-output",
		"--env-json", envFile,
		"--port", strconv.Itoa(svcPort))

	iInfo = intercept.Info{}
	require.NoError(json.Unmarshal([]byte(stdout), &iInfo))
	s.Equal(iInfo.Environment["TAG"], "socat")

	itest.PingInterceptedEchoServer(ctx, svc, "80")
	mountPoint = iInfo.Mount.LocalDir
	dataFile = filepath.Join(mountPoint, "usr", "share", "data", "text")
	content, err = os.ReadFile(dataFile)
	require.NoError(err, "unable to read", dataFile)
	s.Equal("Hello from socat\n", string(content))
}

func (s *connectedSuite) Test_InterceptsContainerAndReplace() {
	ctx, cancel := context.WithCancel(dcontext.WithSoftness(s.Context()))
	defer cancel()
	const svc = "echo-secondary"

	svcPort, svcCancel := itest.StartLocalHttpEchoServer(ctx, svc)
	defer svcCancel()

	s.ApplyApp(ctx, svc, "deploy/"+svc)
	defer s.DeleteSvcAndWorkload(ctx, "deployment", svc)
	defer func() {
		s.NoError(s.Kubectl(ctx, "delete", "configmaps", "socat-data", "echo-data"))
	}()

	require := s.Require()
	dir := s.T().TempDir()
	envFile := filepath.Join(dir, "env.json")
	stdout := itest.TelepresenceOk(ctx, "intercept", svc,
		"--output", "json",
		"--detailed-output",
		"--container", "echo",
		"--replace",
		"--env-json", envFile,
		"--port", strconv.Itoa(svcPort))
	defer itest.TelepresenceOk(ctx, "leave", svc)

	var iInfo intercept.Info
	require.NoError(json.Unmarshal([]byte(stdout), &iInfo))
	s.CapturePodLogs(ctx, svc, "traffic-agent", s.AppNamespace())
	itest.PingInterceptedEchoServer(ctx, svc, "80")

	// Check that the env stems from the targeted container
	s.Equal(iInfo.Environment["TAG"], "echo-server")
	mountPoint := iInfo.Mount.LocalDir
	dataDir := filepath.Join(mountPoint, "usr", "share", "data")
	st, err := os.Stat(dataDir)
	require.NoError(err, "mount of %s should be successful", dataDir)
	require.True(st.IsDir())
	dataFile := filepath.Join(dataDir, "text")
	content, err := os.ReadFile(dataFile)
	require.NoErrorf(err, "unable to read %s", dataFile)
	s.Equal("Hello from echo\n", string(content))

	// Verify that the container is replaced.
	stdout, err = s.KubectlOut(ctx, "get", "pod", "-l", "app="+svc, "-o", "json")
	require.NoError(err)
	items := struct {
		ApiVersion string     `json:"apiVersion"`
		Items      []core.Pod `json:"items"`
	}{}
	require.NoError(json.Unmarshal([]byte(stdout), &items))
	pods := items.Items
	var echoContainer *core.Container
	for pi := range pods {
		cns := pods[pi].Spec.Containers
		for ci := range cns {
			container := &cns[ci]
			if container.Name == "echo" {
				echoContainer = container
				break
			}
		}
	}
	if s.ManagerIsVersion(">2.21.x") {
		require.Nil(echoContainer)
	} else {
		require.NotNil(echoContainer)
		require.Equal(echoContainer.Image, "alpine:latest")
		require.True(slices.Equal(echoContainer.Args, []string{"sleep", "infinity"}))
	}

	// Intercept again, this time without the --container flag
	itest.TelepresenceOk(ctx, "leave", svc)
	stdout = itest.TelepresenceOk(ctx, "intercept", svc,
		"--output", "json",
		"--detailed-output",
		"--env-json", envFile,
		"--port", strconv.Itoa(svcPort))

	iInfo = intercept.Info{}
	require.NoError(json.Unmarshal([]byte(stdout), &iInfo))
	s.Equal(iInfo.Environment["TAG"], "socat")

	itest.PingInterceptedEchoServer(ctx, svc, "80")
	mountPoint = iInfo.Mount.LocalDir
	dataFile = filepath.Join(mountPoint, "usr", "share", "data", "text")
	content, err = os.ReadFile(dataFile)
	require.NoError(err, "unable to read", dataFile)
	s.Equal("Hello from socat\n", string(content))
}
