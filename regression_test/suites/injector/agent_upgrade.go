package injector

import (
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AgentUpgrade proves that the traffic-manager replaces pods that carry a
// traffic-agent of another image than the one it is configured with.
type AgentUpgrade struct {
	rt.Suite
}

func init() {
	rt.Register(&AgentUpgrade{}, rt.InArea("injector"), rt.NeedsManager(managers.Default), rt.On("linux"))
}

const (
	// releasedAgentTag is a released tel2 image that the nodes can pull.
	releasedAgentTag = "2.33.4"

	// upgradeTimeout bounds the wait for every pod to be replaced: two
	// pods, paced by the workload's rolling-update settings.
	upgradeTimeout = 4 * time.Minute
)

// Test_AgentImageChangeReplacesPods injects traffic-agents from a released
// image, then switches the manager to the build's own agent image, which is
// what a traffic-manager upgrade does, and expects every pod of the
// workload to be replaced by one that runs the build's agent image.
func (s *AgentUpgrade) Test_AgentImageChangeReplacesPods() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()

	oldImage := "ghcr.io/telepresenceio/tel2:" + releasedAgentTag
	rt.Mutate(t, rt.ManagerFixture(managers.AgentImage("ghcr.io/telepresenceio", "tel2", releasedAgentTag)))

	tpl := workloads.EchoReplicas("agent-upgrade", 2)
	tpl.Annotations = map[string]string{annotation.InjectTrafficAgent: "enabled"}
	wl := freshWorkload(t, ctx, r, ns, tpl)

	oldUIDs := map[string]bool{}
	s.Require().Eventually(func() bool {
		pods, err := podsOf(ctx, r, ns, wl.Name)
		if err != nil || len(pods) != tpl.Replicas {
			return false
		}
		uids := map[string]bool{}
		for i := range pods {
			if !podReady(&pods[i]) || agentImage(&pods[i]) != oldImage {
				return false
			}
			uids[string(pods[i].UID)] = true
		}
		oldUIDs = uids
		return true
	}, agentPollTimeout, agentPollInterval, "every pod should carry a traffic-agent of image %s", oldImage)

	rt.Mutate(t, rt.ManagerFixture(managers.Default))
	newSuffix := "/tel2:" + r.ManagerVersion().String()

	s.Require().Eventually(func() bool {
		pods, err := podsOf(ctx, r, ns, wl.Name)
		if err != nil || len(pods) != tpl.Replicas {
			return false
		}
		for i := range pods {
			if oldUIDs[string(pods[i].UID)] || !podReady(&pods[i]) || !strings.HasSuffix(agentImage(&pods[i]), newSuffix) {
				return false
			}
		}
		return true
	}, upgradeTimeout, agentPollInterval, "every pod should be replaced by one running a traffic-agent with image suffix %s", newSuffix)
}

// agentImage returns the image of the pod's traffic-agent container, or
// the empty string when the pod has none.
func agentImage(pod *corev1.Pod) string {
	for _, c := range pod.Spec.Containers {
		if c.Name == agentContainerName {
			return c.Image
		}
	}
	return ""
}
