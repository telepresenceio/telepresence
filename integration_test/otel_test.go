package integration_test

import (
	"bytes"
	"os"
	"path/filepath"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
)

type otelSuite struct {
	itest.Suite
	itest.NamespacePair
}

func (s *otelSuite) SuiteName() string {
	return "Otel"
}

func init() {
	itest.AddNamespacePairSuite("-otel", func(h itest.NamespacePair) itest.TestingSuite {
		return &otelSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *otelSuite) SetupSuite() {
	if _, ok := os.LookupEnv("HTTP_INTERCEPT_STRESS_TEST"); !ok {
		s.T().Skip("Run this stress manually. It's too demanding for the CI infrastructure.")
		return
	}

	s.Suite.SetupSuite()
	ctx := s.Context()

	cmd := itest.Command(ctx, "helm", "upgrade", "--install", "cert-manager", "jetstack/cert-manager", "--namespace", s.AppNamespace(), "--version", "v1.8.0", "--set", "installCRDs=true")
	out, err := cmd.CombinedOutput()
	s.Require().NoError(err, "helm repo add: %s", out)
	defer func() {
		if err != nil {
			_ = itest.Run(ctx, "helm", "uninstall", "cert-manager", "--namespace", s.AppNamespace())
		}
	}()

	otelDir := filepath.Join("testdata", "otel")
	cmd = itest.Command(ctx, "helm", "upgrade", "--install", "otel-operator", "open-telemetry/opentelemetry-operator",
		"--namespace", s.AppNamespace(),
		"--create-namespace",
		"--set", "manager.collectorImage.repository=otel/opentelemetry-collector-k8s",
		"--set", "admissionWebhooks.certManager.enabled=false",
		"--set", "admissionWebhooks.autoGenerateCert.enabled=true",
		"--set", "manager.extraArgs={--enable-go-instrumentation}",
		"--values", filepath.Join(otelDir, "helm-yamls", "otel-operator.yml"),
	)
	out, err = cmd.CombinedOutput()
	s.Require().NoError(err, "helm install", string(out))
	defer func() {
		if err != nil {
			_ = itest.Run(ctx, "helm", "uninstall", "otel-operator", "--namespace", s.AppNamespace())
		}
	}()

	cmd = itest.Command(ctx, "helm", "upgrade", "--install", "--namespace", s.AppNamespace(), "alloy", "grafana/alloy")
	out, err = cmd.CombinedOutput()
	s.Require().NoError(err, "helm install", string(out))
	defer func() {
		if err != nil {
			_ = itest.Run(ctx, "helm", "uninstall", "alloy", "--namespace", s.AppNamespace())
		}
	}()

	// Wait for cert-manager to be ready. The job might have been completed already.
	_ = s.Kubectl(ctx, "wait", "--for=condition=complete", "--timeout=60s", "job/cert-manager-startupapicheck")
	_ = s.RolloutStatusWait(ctx, "deploy/cert-manager-webhook")
	_ = s.RolloutStatusWait(ctx, "deploy/cert-manager")
	time.Sleep(30 * time.Second)

	var instrumentationData []byte
	instrumentationData, err = os.ReadFile(filepath.Join(otelDir, "instrumentation.yml"))
	s.Require().NoError(err)
	instrumentationData = bytes.ReplaceAll(instrumentationData, []byte("__NAMESPACE__"), []byte(s.AppNamespace()))
	err = s.Kubectl(dos.WithStdin(ctx, bytes.NewReader(instrumentationData)), "apply", "-f", "-")
	s.Require().NoError(err)

	var so string
	so, err = s.TelepresenceHelmInstall(ctx, false, "--set", "logLevel=trace", "--set", "agentInjector.mutationAware=false")
	s.Require().NoError(err, "telepresence install", so)
}

func (s *otelSuite) TearDownSuite() {
	ctx := s.Context()
	s.UninstallTrafficManager(ctx, s.ManagerNamespace())
	cmd := itest.Command(ctx, "helm", "uninstall", "otel-operator", "--namespace", s.AppNamespace())
	out, err := cmd.CombinedOutput()
	s.NoError(err, "helm uninstall otel-operator", string(out))
	cmd = itest.Command(ctx, "helm", "uninstall", "cert-manager", "--namespace", s.AppNamespace())
	out, err = cmd.CombinedOutput()
	s.NoError(err, "helm uninstall cert-manager", string(out))
	cmd = itest.Command(ctx, "helm", "uninstall", "alloy", "--namespace", s.AppNamespace())
	out, err = cmd.CombinedOutput()
	s.NoError(err, "helm uninstall alloy", string(out))
}
