package integration_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
)

func (s *helmSuite) applyPolicyApp(ctx context.Context, name, namespace string, wg *sync.WaitGroup) {
	defer wg.Done()
	s.T().Helper()
	manifest := filepath.Join("testdata", "k8s", name+".yaml")
	s.NoError(itest.Kubectl(ctx, namespace, "apply", "-f", manifest), "failed to apply %s", manifest)
	s.NoError(itest.RolloutStatusWait(ctx, namespace, "deploy/"+name))
}

func (s *helmSuite) assertInjected(ctx context.Context, name, namespace string, present bool, wg *sync.WaitGroup) {
	defer wg.Done()
	s.T().Helper()
	out, err := itest.KubectlOut(ctx, namespace, "get", "pods", "-l", "app="+name, "-o", "jsonpath={.items.*.spec.containers[?(@.name=='traffic-agent')].image}")
	s.NoError(err)
	if present {
		s.Contains(out, "/tel2:")
	} else {
		s.NotContains(out, "/tel2:")
	}
}

func (s *helmSuite) injectPolicyTest(origCtx context.Context, policy agentconfig.InjectPolicy) {
	namespace := fmt.Sprintf("%s-%s", strings.ToLower(policy.String()), s.Suffix())
	ctx := itest.WithEnv(origCtx, map[string]string{"TELEPRESENCE_MANAGER_NAMESPACE": namespace})
	itest.CreateNamespaces(ctx, namespace)
	defer itest.DeleteNamespaces(ctx, namespace)

	defer func() {
		itest.TelepresenceOk(ctx, "quit", "-ur")
		s.UninstallTrafficManager(ctx, namespace)
	}()
	s.NoError(s.InstallTrafficManager(ctx, map[string]string{"agentInjector.injectPolicy": policy.String()}, namespace))
	itest.TelepresenceOk(ctx, "connect")
	itest.TelepresenceOk(ctx, "loglevel", "debug")
	s.CapturePodLogs(ctx, "app=traffic-manager", "", namespace)

	wg := sync.WaitGroup{}
	wg.Add(3)
	go s.applyPolicyApp(ctx, "pol-enabled", namespace, &wg)
	go s.applyPolicyApp(ctx, "pol-none", namespace, &wg)
	go s.applyPolicyApp(ctx, "pol-disabled", namespace, &wg)
	wg.Wait()
	if s.T().Failed() {
		return
	}

	// No pod should have a traffic-agent at this stage, except for the pol-enabled when the policy is WhenEnabled
	wg.Add(3)
	go s.assertInjected(ctx, "pol-enabled", namespace, policy == agentconfig.WhenEnabled, &wg)
	go s.assertInjected(ctx, "pol-none", namespace, false, &wg)
	go s.assertInjected(ctx, "pol-disabled", namespace, false, &wg)
	wg.Wait()
	if s.T().Failed() {
		return
	}

	// An intercept on the pol-disabled must always fail
	wg.Add(2)
	go func() {
		_, _, err := itest.Telepresence(ctx, "intercept", "--namespace", namespace, "--mount", "false", "pol-disabled", "--", "true")
		s.Error(err)
		s.assertInjected(ctx, "pol-disabled", namespace, false, &wg)
	}()

	go func() {
		_, _, err := itest.Telepresence(ctx, "intercept", "--namespace", namespace, "--mount", "false", "pol-none", "--", "true")
		if policy != agentconfig.OnDemand {
			s.Error(err)
			s.assertInjected(ctx, "pol-none", namespace, false, &wg)
		} else {
			s.NoError(err)
			s.assertInjected(ctx, "pol-none", namespace, true, &wg)
		}
	}()

	if policy != agentconfig.WhenEnabled {
		wg.Add(1)
		go func() {
			_, _, err := itest.Telepresence(ctx, "intercept", "--namespace", namespace, "--mount", "false", "pol-enabled", "--", "true")
			s.NoError(err)
			s.assertInjected(ctx, "pol-enabled", namespace, true, &wg)
		}()
	}
	wg.Wait()
}

func (s *helmSuite) TestInjectPolicy() {
	ctx := s.Context()
	itest.TelepresenceOk(ctx, "quit", "-ur")
	defer func() {
		itest.TelepresenceOk(ctx, "connect")
	}()

	for _, policy := range []agentconfig.InjectPolicy{agentconfig.OnDemand, agentconfig.OnDemandWhenEnabled, agentconfig.WhenEnabled} {
		s.Run(policy.String(), func() {
			s.injectPolicyTest(s.Context(), policy)
		})
	}
}
