package integration_test

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/labels"
)

type nsSuite struct {
	nss []namespaceData
	itest.Suite
}

func (s *nsSuite) SuiteName() string {
	return "Namespaces"
}

func init() {
	itest.AddClusterSuite(func(ctx context.Context) itest.TestingSuite {
		return &nsSuite{Suite: itest.Suite{Harness: itest.NewContextHarness(ctx)}}
	})
}

const namespaceTpl = `
apiVersion: v1
kind: Namespace
metadata:
  name: {{.Name}}
  labels:
    purpose: tp-cli-testing
{{- with .Labels }}
{{ toYaml . | nindent 4 }}
{{- end }}`

type namespaceData struct {
	Name   string
	Labels map[string]string
}

func (s *nsSuite) SetupSuite() {
	if !(s.ClientIsVersion(">2.21.x") && s.ManagerIsVersion(">2.21.x")) {
		s.T().Skip("Not part of compatibility tests. Namespace selector was introduced in 2.22.0")
	}
	s.nss = []namespaceData{
		{
			"manager",
			nil,
		},
		{
			"alpha",
			map[string]string{
				"phase":  "alpha",
				"deploy": "dev",
			},
		},
		{
			"beta",
			map[string]string{
				"phase":  "beta",
				"deploy": "dev",
			},
		},
		{
			"gamma",
			map[string]string{
				"phase":  "beta",
				"deploy": "dev",
			},
		},
		{
			"rc",
			map[string]string{
				"phase":  "rc",
				"deploy": "staging",
			},
		},
		{
			"ga",
			map[string]string{
				"phase":  "ga",
				"deploy": "prod",
			},
		},
		{
			"patch",
			map[string]string{
				"phase":  "patch",
				"deploy": "prod",
			},
		},
	}
	ctx := s.Context()
	wg := new(sync.WaitGroup)
	wg.Add(len(s.nss))
	for _, ns := range s.nss {
		go func() {
			defer wg.Done()
			data, err := itest.EvalTemplate(namespaceTpl, ns)
			s.NoError(err)
			s.NoError(itest.Kubectl(dos.WithStdin(ctx, bytes.NewReader(data)), "", "apply", "-f", "-"))
			itest.ApplyEchoService(ctx, "echo", ns.Name, 80)
		}()
	}
	wg.Wait()
	err := itest.Kubectl(ctx, s.nss[0].Name, "apply", "-f", filepath.Join(itest.GetOSSRoot(ctx), "testdata", "k8s", "client_sa.yaml"))
	s.NoError(err, "failed to create connect ServiceAccount")
}

func (s *nsSuite) AmendSuiteContext(ctx context.Context) context.Context {
	return itest.WithUser(ctx, "manager:"+itest.TestUser)
}

func (s *nsSuite) managerNamespace() string {
	return s.nss[0].Name
}

func (s *nsSuite) TearDownSuite() {
	ctx := s.Context()
	wg := new(sync.WaitGroup)
	wg.Add(len(s.nss))
	for _, ns := range s.nss {
		go func() {
			defer wg.Done()
			_ = itest.Kubectl(ctx, "", "delete", "ns", ns.Name)
		}()
	}
	wg.Wait()
}

func (s *nsSuite) Test_NamespacesClusterWide() {
	ctx := s.Context()
	ctx = itest.WithNamespaces(ctx, &itest.Namespaces{
		Namespace: s.managerNamespace(),
	})
	s.TelepresenceHelmInstallOK(ctx, false)
	defer s.UninstallTrafficManager(ctx, "manager")

	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.managerNamespace(), "--namespace", "alpha")
	defer itest.TelepresenceDisconnectOk(ctx)

	st := itest.TelepresenceStatusOk(ctx)
	s.Greater(len(st.UserDaemon.MappedNamespaces), len(s.nss))
}

func (s *nsSuite) Test_NamespacesDynamic() {
	ctx := itest.WithNamespaces(s.Context(), &itest.Namespaces{
		Namespace: s.managerNamespace(),
		Selector: &labels.Selector{
			MatchExpressions: []*labels.Requirement{
				{
					Key:      "phase",
					Operator: labels.OperatorIn,
					Values:   []string{"alpha"},
				},
				{
					Key:      "deploy",
					Operator: labels.OperatorIn,
					Values:   []string{"dev"},
				},
			},
		},
	})
	s.TelepresenceHelmInstallOK(ctx, false)
	defer s.UninstallTrafficManager(ctx, "manager")

	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.managerNamespace(), "--namespace", "alpha")
	defer itest.TelepresenceDisconnectOk(ctx)

	rq := s.Require()
	st := itest.TelepresenceStatusOk(ctx)
	rq.Len(st.UserDaemon.MappedNamespaces, 1)

	itest.TelepresenceDisconnectOk(ctx)
	_, se, err := itest.Telepresence(ctx, "connect", "--manager-namespace", s.managerNamespace(), "--namespace", "beta")
	rq.Error(err)
	rq.Contains(se, "beta is not managed")

	// Switch to just using label "deploy=dev"
	ctx = itest.WithNamespaces(s.Context(), &itest.Namespaces{
		Namespace: s.managerNamespace(),
		Selector: &labels.Selector{
			MatchExpressions: []*labels.Requirement{{
				Key:      "deploy",
				Operator: labels.OperatorIn,
				Values:   []string{"dev"},
			}},
		},
	})

	restartCount := func() int {
		pods := itest.RunningPods(ctx, agentconfig.ManagerAppName, s.managerNamespace())
		if len(pods) == 1 {
			for _, cs := range pods[0].Status.ContainerStatuses {
				if cs.Name == agentconfig.ManagerAppName {
					return int(cs.RestartCount)
				}
			}
		}
		return -1
	}

	rq.Equal(0, restartCount())
	s.TelepresenceHelmInstallOK(ctx, true)

	// A dynamic change of namespaces should not result in a traffic-manager restart
	rq.Equal(0, restartCount())

	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.managerNamespace(), "--namespace", "beta")
	st = itest.TelepresenceStatusOk(ctx)
	rq.Len(st.UserDaemon.MappedNamespaces, 3)
	itest.TelepresenceDisconnectOk(ctx)

	// Add yet another namespace that matches "deploy=dev"
	data, err := itest.EvalTemplate(namespaceTpl, namespaceData{
		"delta",
		map[string]string{
			"phase":  "test",
			"deploy": "dev",
		},
	})
	rq.NoError(err)
	rq.NoError(itest.Kubectl(dos.WithStdin(ctx, bytes.NewReader(data)), "", "apply", "-f", "-"))
	defer func() {
		s.NoError(itest.Kubectl(ctx, "", "delete", "namespace", "delta"))
	}()

	// Upgrade, to ensure that the proper roles and rolebindings are installed.
	s.TelepresenceHelmInstallOK(ctx, true)
	rq.Equal(0, restartCount())

	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.managerNamespace(), "--namespace", "delta")
	st = itest.TelepresenceStatusOk(ctx)
	rq.Len(st.UserDaemon.MappedNamespaces, 4)

	itest.ApplyEchoService(ctx, "echo", "delta", 80)

	// Check that list output includes the service from "delta"
	lst := itest.TelepresenceOk(ctx, "list")
	rq.Contains(lst, "deployment echo:")
	itest.TelepresenceDisconnectOk(ctx)

	// Delete and recreate the namespace
	s.NoError(itest.Kubectl(ctx, "", "delete", "namespace", "delta"))
	rq.NoError(itest.Kubectl(dos.WithStdin(ctx, bytes.NewReader(data)), "", "apply", "-f", "-"))

	// Upgrade, to ensure that the proper roles and rolebindings are installed.
	s.TelepresenceHelmInstallOK(ctx, true)
	rq.Equal(0, restartCount())

	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.managerNamespace(), "--namespace", "delta")
	st = itest.TelepresenceStatusOk(ctx)
	rq.Len(st.UserDaemon.MappedNamespaces, 4)

	itest.ApplyEchoService(ctx, "echo", "delta", 80)

	// Check that list output still includes the service from "delta"
	lst = itest.TelepresenceOk(ctx, "list")
	rq.Contains(lst, "deployment echo:")
	itest.TelepresenceDisconnectOk(ctx)
}

func (s *nsSuite) Test_NamespacesStatic() {
	ctx := itest.WithNamespaces(s.Context(), &itest.Namespaces{
		Namespace: s.managerNamespace(),
		Selector: &labels.Selector{
			MatchExpressions: []*labels.Requirement{
				{
					Key:      labels.NameLabelKey,
					Operator: labels.OperatorIn,
					Values:   []string{"alpha"},
				},
			},
		},
	})
	s.TelepresenceHelmInstallOK(ctx, false)
	defer s.UninstallTrafficManager(ctx, "manager")

	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.managerNamespace(), "--namespace", "alpha")
	defer itest.TelepresenceDisconnectOk(ctx)

	rq := s.Require()
	st := itest.TelepresenceStatusOk(ctx)
	rq.Len(st.UserDaemon.MappedNamespaces, 1)

	itest.TelepresenceDisconnectOk(ctx)
	_, se, err := itest.Telepresence(ctx, "connect", "--manager-namespace", s.managerNamespace(), "--namespace", "beta")
	rq.Error(err)
	rq.Contains(se, "beta is not managed")

	// Switch to just using both alpha and beta
	ctx = itest.WithNamespaces(s.Context(), &itest.Namespaces{
		Namespace: s.managerNamespace(),
		Selector: &labels.Selector{
			MatchExpressions: []*labels.Requirement{{
				Key:      labels.NameLabelKey,
				Operator: labels.OperatorIn,
				Values:   []string{"alpha", "beta"},
			}},
		},
	})

	getPodName := func() (podName string) {
		rq.Eventually(func() bool {
			pods := itest.RunningPods(ctx, agentconfig.ManagerAppName, s.managerNamespace())
			if len(pods) == 1 {
				podName = pods[0].Name
				return true
			}
			return false
		}, 16*time.Second, 4*time.Second)
		return podName
	}

	tmPodName := getPodName()
	s.TelepresenceHelmInstallOK(ctx, true)

	// A static change must force a restart of the traffic-manager, or the new permissions will not come into effect.
	tmPodNameAfter := getPodName()
	rq.NotEqual(tmPodName, tmPodNameAfter)

	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.managerNamespace(), "--namespace", "beta")
	st = itest.TelepresenceStatusOk(ctx)
	rq.Len(st.UserDaemon.MappedNamespaces, 2)
	itest.TelepresenceDisconnectOk(ctx)
}

func (s *nsSuite) Test_MultiNamespaceHTTPIntercepts() {
	if !(s.ManagerIsVersion(">2.24.x") && s.ClientIsVersion(">2.24.x")) {
		s.T().Skip("HTTP intercepts require Telepresence 2.25.0 or later")
	}
	ctx := itest.WithNamespaces(s.Context(), &itest.Namespaces{
		Namespace: s.managerNamespace(),
		Selector: &labels.Selector{
			MatchExpressions: []*labels.Requirement{{
				Key:      labels.NameLabelKey,
				Operator: labels.OperatorIn,
				Values:   []string{"alpha", "beta"},
			}},
		},
	})
	s.TelepresenceHelmInstallOK(ctx, false)
	defer s.UninstallTrafficManager(ctx, "manager")

	itest.TelepresenceOk(ctx, "connect",
		"--manager-namespace", s.managerNamespace(),
		"--namespace", "alpha",
		"--mapped-namespaces", "alpha,beta")
	defer itest.TelepresenceDisconnectOk(ctx)

	localPortA, cancelA := itest.StartLocalHttpEchoServer(ctx, "tp-alpha-local")
	defer cancelA()
	localPortB, cancelB := itest.StartLocalHttpEchoServer(ctx, "tp-beta-local")
	defer cancelB()

	itest.TelepresenceOk(ctx, "intercept", "tp-alpha-local",
		"--workload", "echo",
		"--namespace", "alpha",
		"--http-header", "x-tp-ns=a",
		"--port", fmt.Sprintf("%d:80", localPortA),
		"--mount=false")
	defer itest.TelepresenceOk(ctx, "leave", "tp-alpha-local")

	itest.TelepresenceOk(ctx, "intercept", "tp-beta-local",
		"--workload", "echo",
		"--namespace", "beta",
		"--http-header", "x-tp-ns=b",
		"--port", fmt.Sprintf("%d:80", localPortB),
		"--mount=false")
	defer itest.TelepresenceOk(ctx, "leave", "tp-beta-local")

	rq := s.Require()
	curl := func(host, header string) (string, error) {
		args := []string{"curl", "--silent", "--max-time", "2"}
		if header != "" {
			args = append(args, "-H", header)
		}
		args = append(args, "http://"+host)
		stdout, stderr, err := itest.Telepresence(ctx, args...)
		if err != nil {
			return stderr, err
		}
		return strings.TrimSpace(stdout), nil
	}
	expectCurl := func(host, header, expected string) {
		rq.Eventually(func() bool {
			out, err := curl(host, header)
			return err == nil && out == expected
		}, time.Minute, 5*time.Second)
	}

	expectCurl("echo.alpha", "x-tp-ns: a", "tp-alpha-local from intercept at /")
	expectCurl("echo.beta", "x-tp-ns: b", "tp-beta-local from intercept at /")

	alphaOut, err := curl("echo.alpha", "")
	rq.NoError(err)
	rq.NotContains(alphaOut, "tp-alpha-local from intercept")
	betaOut, err := curl("echo.beta", "")
	rq.NoError(err)
	rq.NotContains(betaOut, "tp-beta-local from intercept")
}
