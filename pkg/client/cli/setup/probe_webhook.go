package setup

import (
	"context"

	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telepresenceio/clog"
)

// probeWebhook is P4: whether the agent-injector's mutating webhook can be
// created, plus a heuristic for clusters where the API server cannot reach an
// in-cluster Service.
func (p *Prober) probeWebhook(ctx context.Context, provider string) WebhookFacts {
	facts := WebhookFacts{
		CanCreate: p.singleAccessCheck(ctx, &authv1.ResourceAttributes{
			Verb:     "create",
			Group:    "admissionregistration.k8s.io",
			Resource: "mutatingwebhookconfigurations",
		}),
	}
	if provider == "eks" {
		facts.ReachabilityConcern = p.eksReachabilityConcern(ctx)
	}
	return facts
}

// eksReachabilityConcern flags the known EKS-with-non-VPC-CNI shape in which
// the control plane cannot reach an in-cluster Service. Any failure to list
// the DaemonSets is skipped after a debug log: it isn't evidence either way.
func (p *Prober) eksReachabilityConcern(ctx context.Context) string {
	list, err := p.KubeClient.AppsV1().DaemonSets("kube-system").List(ctx, metav1.ListOptions{})
	if err != nil {
		if !apierrors.IsForbidden(err) {
			clog.Debugf(ctx, "unable to list kube-system daemonsets: %v", err)
		}
		return ""
	}
	hasCalico, hasAWSNode := false, false
	for _, ds := range list.Items {
		switch ds.Name {
		case "calico-node":
			hasCalico = true
		case "aws-node":
			hasAWSNode = true
		}
	}
	if hasCalico && !hasAWSNode {
		return "EKS with a non-VPC CNI: the API server may not reach the agent-injector Service; " +
			"consider agentInjector.service.type=NodePort with webhook.url"
	}
	return ""
}
