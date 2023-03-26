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

func (s *installSuite) applyPolicyApp(ctx context.Context, name, namespace string, wg *sync.WaitGroup) {
	defer wg.Done()
	s.T().Helper()
	manifest := filepath.Join("testdata", "k8s", name+".yaml")
	s.NoError(itest.Kubectl(ctx, namespace, "apply", "-f", manifest), "failed to apply %s", manifest)
	s.NoError(itest.RolloutStatusWait(ctx, namespace, "deploy/"+name))
}

func (s *installSuite) assertInjected(ctx context.Context, name, namespace string, present bool, wg *sync.WaitGroup) {
	defer wg.Done()
	s.T().Helper()
	out, err := itest.KubectlOut(ctx, namespace, "get", "pods", "-l", "app="+name, "-o", "jsonpath={.items.*.spec.containers[?(@.name=='traffic-agent')].image}")
	s.NoError(err)
	n := "tel2"
	if ai := itest.GetAgentImage(ctx); ai != nil {
		n = ai.Name
	}
	n = "/" + n + ":"
	if present {
		s.Contains(out, n)
	} else {
		s.NotContains(out, n)
	}
}

func (s *installSuite) injectPolicyTest(ctx context.Context, policy agentconfig.InjectPolicy) {
	namespace := fmt.Sprintf("%s-%s", strings.ToLower(policy.String()), s.Suffix())
	itest.CreateNamespaces(ctx, namespace)
	defer itest.DeleteNamespaces(ctx, namespace)

	ctx = itest.WithNamespaces(ctx, &itest.Namespaces{
		Namespace:         namespace,
		ManagedNamespaces: []string{namespace},
	})
	s.NoError(s.TelepresenceHelmInstall(ctx, false, "--set", "agentInjector.injectPolicy="+policy.String()))
	defer s.UninstallTrafficManager(ctx, namespace)

	ctx = itest.WithUser(ctx, namespace+":"+itest.TestUser)
	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", namespace)
	defer itest.TelepresenceOk(ctx, "quit", "-s")

	itest.TelepresenceOk(ctx, "loglevel", "debug")

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
	go s.assertInjected(ctx, "pol-enabled", namespace, true, &wg)   // always injected in advance
	go s.assertInjected(ctx, "pol-none", namespace, false, &wg)     // never injected in advance
	go s.assertInjected(ctx, "pol-disabled", namespace, false, &wg) // never injected
	wg.Wait()
	if s.T().Failed() {
		return
	}

	// An intercept on the pol-disabled must always fail
	wg.Add(1)
	go func() {
		_, _, err := itest.Telepresence(ctx, "intercept", "--namespace", namespace, "--mount", "false", "pol-disabled", "--", "true")
		s.Error(err)
		s.assertInjected(ctx, "pol-disabled", namespace, false, &wg)
	}()

	// for OnDemand, an intercept on the pol-none must succeed inject the agent
	if policy == agentconfig.OnDemand {
		wg.Add(1)
		_, _, err := itest.Telepresence(ctx, "intercept", "--namespace", namespace, "--mount", "false", "pol-none", "--", "true")
		s.NoError(err)
		s.assertInjected(ctx, "pol-none", namespace, true, &wg)
	}
	wg.Wait()
}

func (s *installSuite) TestInjectPolicy() {
	for _, policy := range []agentconfig.InjectPolicy{agentconfig.OnDemand, agentconfig.WhenEnabled} {
		s.Run(policy.String(), func() {
			s.injectPolicyTest(s.Context(), policy)
		})
	}
}
