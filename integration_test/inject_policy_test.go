package integration_test

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
)

func (is *installSuite) applyPolicyApp(ctx context.Context, name, namespace string, wg *sync.WaitGroup) {
	defer wg.Done()
	is.T().Helper()
	manifest := filepath.Join("testdata", "k8s", name+".yaml")
	is.NoError(itest.Kubectl(ctx, namespace, "apply", "-f", manifest), "failed to apply %s", manifest)
	is.NoError(itest.RolloutStatusWait(ctx, namespace, "deploy/"+name))
}

func (is *installSuite) assertInjected(ctx context.Context, name, namespace string, present bool, wg *sync.WaitGroup) {
	defer wg.Done()
	is.T().Helper()
	out, err := itest.KubectlOut(ctx, namespace, "get", "pods", "-l", "app="+name, "-o", "jsonpath={.items.*.spec.containers[?(@.name=='traffic-agent')].image}")
	is.NoError(err)
	n := "tel2"
	if ai := itest.GetAgentImage(ctx); ai != nil {
		n = ai.Name
	}
	n = "/" + n + ":"
	if present {
		is.Contains(out, n)
	} else {
		is.NotContains(out, n)
	}
}

func (is *installSuite) injectPolicyTest(ctx context.Context, policy agentconfig.InjectPolicy) {
	namespace := fmt.Sprintf("%s-%s", strings.ToLower(policy.String()), is.Suffix())
	itest.CreateNamespaces(ctx, namespace)
	defer itest.DeleteNamespaces(ctx, namespace)

	ctx = itest.WithNamespaces(ctx, &itest.Namespaces{
		Namespace:         namespace,
		ManagedNamespaces: []string{namespace},
	})
	is.TelepresenceHelmInstallOK(ctx, false, "--set", "agentInjector.injectPolicy="+policy.String())
	defer is.UninstallTrafficManager(ctx, namespace)

	ctx = itest.WithUser(ctx, namespace+":"+itest.TestUser)
	itest.TelepresenceOk(ctx, "connect", "--namespace", namespace, "--manager-namespace", namespace)
	defer itest.TelepresenceOk(ctx, "quit", "-s")

	itest.TelepresenceOk(ctx, "loglevel", "debug")

	wg := sync.WaitGroup{}
	wg.Add(3)
	go is.applyPolicyApp(ctx, "pol-enabled", namespace, &wg)
	go is.applyPolicyApp(ctx, "pol-none", namespace, &wg)
	go is.applyPolicyApp(ctx, "pol-disabled", namespace, &wg)
	wg.Wait()
	if is.T().Failed() {
		return
	}

	// No pod should have a traffic-agent at this stage, except for the pol-enabled when the policy is WhenEnabled
	wg.Add(3)
	go is.assertInjected(ctx, "pol-enabled", namespace, true, &wg)   // always injected in advance
	go is.assertInjected(ctx, "pol-none", namespace, false, &wg)     // never injected in advance
	go is.assertInjected(ctx, "pol-disabled", namespace, false, &wg) // never injected
	wg.Wait()
	if is.T().Failed() {
		return
	}

	// An intercept on the pol-disabled must always fail
	wg.Add(1)
	go func() {
		_, _, err := itest.Telepresence(ctx, "intercept", "--mount", "false", "pol-disabled", "--", "true")
		is.Error(err)
		is.assertInjected(ctx, "pol-disabled", namespace, false, &wg)
	}()

	// for OnDemand, an intercept on the pol-none must succeed inject the agent
	if policy == agentconfig.OnDemand {
		wg.Add(1)
		_, _, err := itest.Telepresence(ctx, "intercept", "--mount", "false", "pol-none", "--", "true")
		is.NoError(err)
		is.assertInjected(ctx, "pol-none", namespace, true, &wg)
	}
	wg.Wait()
}

func (is *installSuite) TestInjectPolicy() {
	for _, policy := range []agentconfig.InjectPolicy{agentconfig.OnDemand, agentconfig.WhenEnabled} {
		is.Run(policy.String(), func() {
			is.injectPolicyTest(is.Context(), policy)
		})
	}
}

func (is *installSuite) applyMultipleServices(svcCount int) {
	is.applyOrDeleteMultipleServices(svcCount, is.ApplyTemplate, true)
}

func (is *installSuite) deleteMultipleServices(svcCount int) {
	is.applyOrDeleteMultipleServices(svcCount, is.DeleteTemplate, false)
}

func (is *installSuite) applyOrDeleteMultipleServices(svcCount int, applyOrDelete func(context.Context, string, any), wait bool) {
	ctx := is.Context()
	wg := sync.WaitGroup{}
	wg.Add(svcCount)
	for i := range svcCount {
		svc := fmt.Sprintf("quote-%d", i)
		go func() {
			defer wg.Done()
			k8s := filepath.Join("testdata", "k8s")
			applyOrDelete(ctx, filepath.Join(k8s, "generic.goyaml"), &itest.Generic{
				Name:     svc,
				Registry: "datawire",
				Image:    "quote:0.5.0",
				Annotations: map[string]string{
					agentconfig.InjectAnnotation: "enabled",
				},
			})
			if wait {
				is.NoError(is.RolloutStatusWait(ctx, "deploy/"+svc))
			}
		}()
	}
	wg.Wait()
}

func (is *installSuite) Test_MultiOnDemandInjectOnInstall() {
	svcCount := 25
	if runtime.GOOS != "linux" {
		// The GitHub runner is probably using Colima for Kubernetes and running with limited
		// resources.
		svcCount = 10
	}
	ctx := is.Context()

	// First create the pods with inject annotation
	is.applyMultipleServices(svcCount)
	defer is.deleteMultipleServices(svcCount)

	// Then install the traffic-manager
	is.TelepresenceHelmInstallOK(ctx, false)

	// And check that all pods receive a traffic-agent
	is.Eventually(func() bool {
		ras := itest.RunningPodsWithAgents(ctx, "quote-", is.AppNamespace())
		return len(ras) == svcCount
	}, 60*time.Second, 5*time.Second)

	// Uninstall the traffic-manager and check that all pods traffic-agent is removed
	is.UninstallTrafficManager(ctx, is.ManagerNamespace())
	is.Eventually(func() bool {
		ras := itest.RunningPodsWithAgents(ctx, "quote-", is.AppNamespace())
		return len(ras) == 0
	}, 120*time.Second, 5*time.Second)
}

func (is *installSuite) Test_MultiOnDemandInjectOnApply() {
	svcCount := 25
	if runtime.GOOS != "linux" {
		// The GitHub runner is probably using Colima for Kubernetes and running with limited
		// resources.
		svcCount = 10
	}
	ctx := is.Context()

	// First install the traffic-manager
	is.TelepresenceHelmInstallOK(ctx, false)
	defer is.UninstallTrafficManager(ctx, is.ManagerNamespace())

	// Then create the pods with inject annotation
	is.applyMultipleServices(svcCount)
	defer is.deleteMultipleServices(svcCount)

	// And check that all pods receive a traffic-agent
	is.Require().Eventually(func() bool {
		ras := itest.RunningPodsWithAgents(ctx, "quote-", is.AppNamespace())
		return len(ras) == svcCount
	}, 60*time.Second, 5*time.Second)
}
