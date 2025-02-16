package integration_test

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/go-json-experiment/json"
	core "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

// Test_InterceptOperationRestoredAfterFailingInject tests that the telepresence-agents
// configmap is kept in sync with installed agents after errors occurs during the actual
// injection of a traffic-agent.
// See ticket https://github.com/telepresenceio/telepresence/issues/3441 for more info.
func (s *singleServiceSuite) Test_InterceptOperationRestoredAfterFailingInject() {
	if !s.ClientIsVersion(">2.21.x") {
		s.T().Skip("Not part of compatibility tests.")
	}
	ctx := s.Context()
	rq := s.Require()

	// Create an intercept and ensure that it lists as intercepted
	stdout := itest.TelepresenceOk(ctx, "intercept", s.ServiceName(), "--mount=false")
	rq.Contains(stdout, "Using Deployment "+s.ServiceName())
	rq.Eventually(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
		return err == nil && regexp.MustCompile(s.ServiceName()+`\s*: intercepted`).MatchString(stdout)
	}, 12*time.Second, 3*time.Second)

	// Leave the intercept. We are now 100% sure that an agent is present in the
	// pod.
	itest.TelepresenceOk(ctx, "leave", s.ServiceName())

	// Break the TLS by temporally disabling the agent-injector service. We do this by the port of the
	// service that the webhook is calling.
	portRestored := false
	wh := "agent-injector-webhook-" + s.ManagerNamespace()
	pmf := `{"webhooks":[{"name": "agent-injector-%s.getambassador.io", "clientConfig": {"service": {"name": "agent-injector", "port": %d}}}]}`
	rq.NoError(itest.Kubectl(ctx, s.ManagerNamespace(), "patch", "mutatingwebhookconfiguration", wh,
		"--patch", fmt.Sprintf(pmf, s.ManagerNamespace(), 8443)))

	// Restore the webhook port when this test ends in case an error occurred that prevented it
	defer func() {
		if !portRestored {
			s.NoError(itest.Kubectl(ctx, s.ManagerNamespace(), "patch", "mutatingwebhookconfiguration", wh,
				"--patch", fmt.Sprintf(pmf, s.ManagerNamespace(), 443)))
		}
	}()

	// Uninstall the agent.
	itest.TelepresenceOk(ctx, "uninstall", s.ServiceName())

	oneContainer := func() bool {
		pods := itest.RunningPodNames(ctx, s.ServiceName(), s.AppNamespace())
		if len(pods) != 1 {
			dlog.Infof(ctx, "got %d pods", len(pods))
			return false
		}
		podJSON, err := s.KubectlOut(ctx, "get", "pod", pods[0], "--output", "json")
		if err != nil {
			dlog.Errorf(ctx, "unable to get pod %s: %v", pods[0], err)
			return false
		}
		var pod core.Pod
		err = json.Unmarshal([]byte(podJSON), &pod)
		if err != nil {
			dlog.Errorf(ctx, "unable to parse json of pod %s: %v", pods[0], err)
			return false
		}
		nc := len(pod.Spec.Containers)
		if nc == 1 {
			return true
		}
		dlog.Errorf(ctx, "pod %s has %d containers", pods[0], nc)
		return false
	}

	// Verify that the pod has no agent
	rq.Eventually(oneContainer, 30*time.Second, 3*time.Second)

	// Now try to intercept. This attempt will timeout because the agent is never injected.
	_, _, err := itest.Telepresence(ctx, "intercept", s.ServiceName(), "--mount=false")
	// Wait for the intercept call to return. It must return an error.
	rq.Error(err)

	// Verify that the pod still has no agent
	rq.True(oneContainer())

	// Restore mutating-webhook operation.
	rq.NoError(itest.Kubectl(ctx, s.ManagerNamespace(), "patch", "mutatingwebhookconfiguration", wh,
		"--patch", fmt.Sprintf(pmf, s.ManagerNamespace(), 443)))
	portRestored = true

	// Verify that intercept works OK again.
	stdout = itest.TelepresenceOk(ctx, "intercept", s.ServiceName(), "--mount=false")
	rq.Contains(stdout, "Using Deployment "+s.ServiceName())
	rq.Eventually(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
		return err == nil && regexp.MustCompile(s.ServiceName()+`\s*: intercepted`).MatchString(stdout)
	}, 12*time.Second, 3*time.Second)
	itest.TelepresenceOk(ctx, "leave", s.ServiceName())
}

// Test_HelmUpgradeWebhookSecret tests that updating the webhook secret doesn't interfere with
// intercept operations.
// See https://github.com/telepresenceio/telepresence/issues/3442 for more info.
func (s *singleServiceSuite) Test_HelmUpgradeWebhookSecret() {
	ctx := s.Context()
	rq := s.Require()

	stdout := itest.TelepresenceOk(ctx, "intercept", s.ServiceName(), "--mount=false")
	rq.Contains(stdout, "Using Deployment "+s.ServiceName())
	rq.Eventually(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
		return err == nil && regexp.MustCompile(s.ServiceName()+`\s*: intercepted`).MatchString(stdout)
	}, 12*time.Second, 3*time.Second)

	s.TelepresenceHelmInstallOK(ctx, true, "--set", "agentInjector.certificate.regenerate=true,agentInjector.certificate.accessMethod=watch,logLevel=debug")
	defer s.RollbackTM(ctx)
	time.Sleep(5 * time.Second)

	// Check that the intercept is still active
	st := itest.TelepresenceStatusOk(ctx)
	rq.Len(st.UserDaemon.Intercepts, 1)
	itest.TelepresenceOk(ctx, "leave", s.ServiceName())

	// Uninstall the agent again. We want to be sure that the webhook kicks in to inject it once
	// we intercept.
	func() {
		defer func() {
			// Restore original user
			itest.TelepresenceDisconnectOk(ctx)
			s.TelepresenceConnect(ctx)
		}()
		itest.TelepresenceDisconnectOk(ctx)
		s.TelepresenceConnect(itest.WithUser(ctx, "default"))
		itest.TelepresenceOk(ctx, "uninstall", s.ServiceName())
	}()
	stdout = itest.TelepresenceOk(ctx, "intercept", s.ServiceName(), "--mount=false")
	rq.Contains(stdout, "Using Deployment "+s.ServiceName())
	rq.Eventually(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
		return err == nil && regexp.MustCompile(s.ServiceName()+`\s*: intercepted`).MatchString(stdout)
	}, 12*time.Second, 3*time.Second)
	itest.TelepresenceOk(ctx, "leave", s.ServiceName())
}

// Test_HelmUpgradeMountedWebhookSecret tests that updating the webhook secret does interfere with
// intercept operations.
func (s *singleServiceSuite) Test_HelmUpgradeMountedWebhookSecret() {
	ctx := s.Context()
	rq := s.Require()

	stdout := itest.TelepresenceOk(ctx, "intercept", s.ServiceName(), "--mount=false")
	rq.Contains(stdout, "Using Deployment "+s.ServiceName())
	rq.Eventually(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
		return err == nil && regexp.MustCompile(s.ServiceName()+`\s*: intercepted`).MatchString(stdout)
	}, 12*time.Second, 3*time.Second)

	s.TelepresenceHelmInstallOK(ctx, true, "--set", "agentInjector.certificate.regenerate=true,agentInjector.certificate.accessMethod=mount,logLevel=debug")
	time.Sleep(5 * time.Second)
	defer func() {
		itest.TelepresenceDisconnectOk(ctx)
		s.RollbackTM(context.WithoutCancel(ctx))
		time.Sleep(5 * time.Second)
		s.TelepresenceConnect(ctx)
	}()

	// Using accessMethod=mount will restart the traffic-manager, so the intercept must be dead at this point
	st := itest.TelepresenceStatusOk(ctx)
	rq.Len(st.UserDaemon.Intercepts, 0)

	// Uninstall the agent again. We want to be sure that the webhook kicks in to inject it once
	// we intercept.
	func() {
		defer func() {
			// Restore original user
			itest.TelepresenceDisconnectOk(ctx)
			s.TelepresenceConnect(ctx)
		}()
		itest.TelepresenceDisconnectOk(ctx)
		s.TelepresenceConnect(itest.WithUser(ctx, "default"))
		itest.TelepresenceOk(ctx, "uninstall", s.ServiceName())
	}()
	stdout = itest.TelepresenceOk(ctx, "intercept", s.ServiceName(), "--mount=false")
	rq.Contains(stdout, "Using Deployment "+s.ServiceName())
	rq.Eventually(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
		return err == nil && regexp.MustCompile(s.ServiceName()+`\s*: intercepted`).MatchString(stdout)
	}, 12*time.Second, 3*time.Second)
	itest.TelepresenceOk(ctx, "leave", s.ServiceName())
}
