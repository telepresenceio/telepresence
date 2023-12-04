package integration_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	core "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
)

// Test_InterceptOperationRestoredAfterFailingInject tests that the telepresence-agents
// configmap is kept in sync with installed agents after errors occurs during the actual
// injection of a traffic-agent.
// See ticket https://github.com/telepresenceio/telepresence/issues/3441 for more info.
func (s *singleServiceSuite) Test_InterceptOperationRestoredAfterFailingInject() {
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

	// Create an intercept again. This must succeed because nothing has changed
	stdout = itest.TelepresenceOk(ctx, "intercept", s.ServiceName(), "--mount=false")
	rq.Contains(stdout, "Using Deployment "+s.ServiceName())
	rq.Eventually(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
		return err == nil && regexp.MustCompile(s.ServiceName()+`\s*: intercepted`).MatchString(stdout)
	}, 12*time.Second, 3*time.Second)
	itest.TelepresenceOk(ctx, "leave", s.ServiceName())

	// Uninstall the agent. This will remove it from the telepresence-agents configmap. It must also
	// uninstall from the agent, even though the webhook is muted, because there will be a rollout and
	// without the webhook, the default is that the pod has no agent.
	func() {
		// TODO: Uninstall should be CLI only and not require a traffic-manager connection
		defer func() {
			// Restore original user
			itest.TelepresenceDisconnectOk(ctx)
			s.TelepresenceConnect(ctx)
		}()
		itest.TelepresenceDisconnectOk(ctx)
		s.TelepresenceConnect(itest.WithUser(ctx, "default"))
		itest.TelepresenceOk(ctx, "uninstall", "--agent", s.ServiceName())
	}()

	oneContainer := func() bool {
		pods := itest.RunningPods(ctx, s.ServiceName(), s.AppNamespace())
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

	// Verify that the pod have no agent
	rq.Eventually(oneContainer, 30*time.Second, 3*time.Second)

	// Now try to intercept. This will make the traffic-manager first inject an entry into the telepresence-agents
	// configmap and the configmap watcher will then cause a subsequent rollout of the workload. That rollout
	// would normally cause the mutating-webhook to kick in. That'll fail now because we disabled it further up.
	iceptDone := make(chan error)
	go func() {
		defer close(iceptDone)
		_, _, err := itest.Telepresence(ctx, "intercept", s.ServiceName(), "--mount=false")
		select {
		case <-ctx.Done():
		case iceptDone <- err:
		}
	}()

	const cmName = "telepresence-agents"
	// Verify that there's a valid entry in the configmap
	rq.Eventually(func() bool {
		cmJSON, err := s.KubectlOut(ctx, "get", "configmap", cmName, "--output", "json")
		if err != nil {
			dlog.Errorf(ctx, "unable to get %s configmap: %v", cmName, err)
			return false
		}
		var cm core.ConfigMap
		err = json.Unmarshal([]byte(cmJSON), &cm)
		if err != nil {
			dlog.Errorf(ctx, "unable to parse json of %s configmap: %v", cmName, err)
			return false
		}
		svcYAML, ok := cm.Data[s.ServiceName()]
		if !ok {
			dlog.Errorf(ctx, "didn't find an entry for %s in %s : %v", s.ServiceName(), cmName, err)
			return false
		}
		sc, err := agentconfig.UnmarshalYAML([]byte(svcYAML))
		if err != nil {
			dlog.Errorf(ctx, "unable to parse yaml of %s in the %s configmap: %v", s.ServiceName(), cmName, err)
		}
		return s.ServiceName() == sc.AgentConfig().AgentName
	}, 30*time.Second, 3*time.Second)

	// Verify that the pod still have no agent
	rq.Eventually(oneContainer, 30*time.Second, 3*time.Second)

	// Wait for the intercept call to return. It must return an error.
	rq.Error(<-iceptDone)

	// Verify that the entry in the configmap has been removed by the traffic-manager.
	cmJSON, err := s.KubectlOut(ctx, "get", "configmap", "telepresence-agents", "--output", "json")
	rq.NoError(err)
	var cm core.ConfigMap
	rq.NoError(json.Unmarshal([]byte(cmJSON), &cm))
	_, ok := cm.Data[s.ServiceName()]
	rq.False(ok)

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

	// Uninstall the agent. We want to be sure that the webhook kicks in to inject it once
	// we intercept.
	func() {
		// TODO: Uninstall should be CLI only and not require a traffic-manager connection
		defer func() {
			// Restore original user
			itest.TelepresenceDisconnectOk(ctx)
			s.TelepresenceConnect(ctx)
		}()
		itest.TelepresenceDisconnectOk(ctx)
		s.TelepresenceConnect(itest.WithUser(ctx, "default"))
		itest.TelepresenceOk(ctx, "uninstall", "--agent", s.ServiceName())
	}()

	s.NoError(s.TelepresenceHelmInstall(ctx, true, "--set", "agentInjector.certificate.regenerate=true,logLevel=debug"))
	time.Sleep(5 * time.Second)

	stdout := itest.TelepresenceOk(ctx, "intercept", s.ServiceName(), "--mount=false")
	rq.Contains(stdout, "Using Deployment "+s.ServiceName())
	rq.Eventually(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
		return err == nil && regexp.MustCompile(s.ServiceName()+`\s*: intercepted`).MatchString(stdout)
	}, 12*time.Second, 3*time.Second)
	itest.TelepresenceOk(ctx, "leave", s.ServiceName())
}
