package integration_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type argoRolloutsSuite struct {
	itest.Suite
	itest.TrafficManager
}

func (s *argoRolloutsSuite) SuiteName() string {
	return "ArgoRollouts"
}

func init() {
	itest.AddTrafficManagerSuite("-argo-rollouts", func(h itest.TrafficManager) itest.TestingSuite {
		return &argoRolloutsSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h}
	})
}

func (s *argoRolloutsSuite) SetupSuite() {
	s.Suite.SetupSuite()
	ctx := s.Context()
	rq := s.Require()
	itest.CreateNamespaces(ctx, "argo-rollouts")
	arExe := filepath.Join(itest.BuildOutput(ctx), "bin", "kubectl-argo-rollouts")
	if runtime.GOOS == "windows" {
		arExe += ".exe"
	}
	if _, err := os.Stat(arExe); err != nil {
		rq.ErrorIs(err, os.ErrNotExist)
		rq.NoError(downloadKubectlArgoRollouts(ctx, arExe))
	}
	out, err := itest.KubectlOut(ctx, "", "argo", "rollouts", "version")
	rq.NoError(err)
	dlog.Info(ctx, out)
	rq.NoError(itest.Kubectl(ctx, "argo-rollouts", "apply", "-f", "https://github.com/argoproj/argo-rollouts/releases/latest/download/install.yaml"))
}

func (s *argoRolloutsSuite) TearDownSuite() {
	itest.DeleteNamespaces(s.Context(), "argo-rollouts")
}

func downloadKubectlArgoRollouts(ctx context.Context, arExe string) error {
	du := fmt.Sprintf(
		"https://github.com/argoproj/argo-rollouts/releases/latest/download/kubectl-argo-rollouts-%s-%s",
		runtime.GOOS, runtime.GOARCH)
	dlog.Infof(ctx, "Downloading %s", du)
	resp, err := http.Get(du)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("expected status 200 OK, got %v", resp.Status)
	}
	arExeFile, err := os.OpenFile(arExe, os.O_WRONLY|os.O_CREATE, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(arExeFile, resp.Body)
	_ = arExeFile.Close()
	return err
}

func (s *argoRolloutsSuite) Test_SuccessfullyInterceptsArgoRollout() {
	ctx := s.Context()
	require := s.Require()

	s.TelepresenceHelmInstallOK(ctx, true, "--set", "workloads.argoRollouts.enabled=true")
	defer s.RollbackTM(ctx)

	tp, svc, port := "Rollout", "echo-argo-rollout", "9094"
	s.ApplyApp(ctx, svc, strings.ToLower(tp)+"/"+svc)
	defer s.DeleteSvcAndWorkload(ctx, "rollout", svc)

	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceDisconnectOk(ctx)
	require.Eventually(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list")
			return err == nil && strings.Contains(stdout, svc)
		},
		6*time.Second, // waitFor
		2*time.Second, // polling interval
	)

	stdout := itest.TelepresenceOk(ctx, "intercept", "--mount", "false", "--port", port, svc)
	require.Contains(stdout, "Using "+tp+" "+svc)
	require.Eventually(func() bool {
		stdout = itest.TelepresenceOk(ctx, "list", "--intercepts")
		return strings.Contains(stdout, svc+": intercepted") && !strings.Contains(stdout, "Volume Mount Point")
	}, 14*time.Second, 2*time.Second)
	s.CapturePodLogs(ctx, svc, "traffic-agent", s.AppNamespace())
	itest.TelepresenceOk(ctx, "leave", svc)
	stdout = itest.TelepresenceOk(ctx, "list", "--intercepts")
	require.NotContains(stdout, svc+": intercepted")

	if !s.ClientIsVersion(">2.21.x") && s.ManagerIsVersion(">2.21.x") {
		// An <2.22.0 client will not be able to uninstall an agent when the traffic-manager is >=2.22.0
		// because the client will attempt to remove the entry in the telepresence-agents configmap. It
		// is no longer present in versions >=2.22.0
		return
	}
	time.Sleep(3 * time.Second)
	itest.TelepresenceOk(ctx, "uninstall", svc)

	require.Eventually(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--agents")
			return err == nil && !strings.Contains(stdout, svc)
		},
		180*time.Second, // waitFor
		6*time.Second,   // polling interval
	)
}

func (s *argoRolloutsSuite) Test_ListsReplicaSetWhenRolloutDisabled() {
	ctx := s.Context()
	require := s.Require()

	tp, svc := "Rollout", "echo-argo-rollout"
	s.ApplyApp(ctx, svc, strings.ToLower(tp)+"/"+svc)
	defer s.DeleteSvcAndWorkload(ctx, "rollout", svc)

	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceDisconnectOk(ctx)

	require.Eventually(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list")
			dlog.Info(ctx, stdout)
			return err == nil && strings.Contains(stdout, svc+"-")
		},
		6*time.Second, // waitFor
		2*time.Second, // polling interval
	)
}
