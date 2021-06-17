package resource

import (
	"context"
	"fmt"

	admreg "k8s.io/api/admissionregistration/v1"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

const agentInjectorWebhookName = install.AgentInjectorName + "-webhook"

type injectorWebhook int

const AgentInjectorWebhook = injectorWebhook(0)

var _ Instance = AgentInjectorWebhook

func (ri injectorWebhook) webhook(ctx context.Context) *admreg.MutatingWebhookConfiguration {
	sec := new(admreg.MutatingWebhookConfiguration)
	sec.TypeMeta = kates.TypeMeta{
		Kind:       "MutatingWebhookConfiguration",
		APIVersion: "admissionregistration.k8s.io/v1",
	}
	sec.ObjectMeta = kates.ObjectMeta{
		Name: fmt.Sprintf("%s-%s", agentInjectorWebhookName, getScope(ctx).namespace),
	}
	return sec
}

func (ri injectorWebhook) Create(ctx context.Context) error {
	timeoutSecs := int32(5)
	sideEffects := admreg.SideEffectClassNone
	failurePolicy := admreg.Ignore
	servicePath := "/traffic-agent"
	scope := admreg.ScopeType("*")
	mwc := ri.webhook(ctx)
	mwc.Webhooks = []admreg.MutatingWebhook{
		{
			Name: "agent-injector.getambassador.io",
			ClientConfig: admreg.WebhookClientConfig{
				Service: &admreg.ServiceReference{
					Namespace: getScope(ctx).namespace,
					Name:      install.AgentInjectorName,
					Path:      &servicePath,
				},
				CABundle: getScope(ctx).caPem,
			},
			Rules: []admreg.RuleWithOperations{
				{
					Operations: []admreg.OperationType{admreg.Create},
					Rule: admreg.Rule{
						APIGroups:   []string{""},
						APIVersions: []string{"v1"},
						Resources:   []string{"pods"},
						Scope:       &scope,
					},
				},
			},
			FailurePolicy:           &failurePolicy,
			SideEffects:             &sideEffects,
			TimeoutSeconds:          &timeoutSecs,
			AdmissionReviewVersions: []string{"v1"},
		},
	}
	return create(ctx, mwc)
}

func (ri injectorWebhook) Exists(ctx context.Context) (bool, error) {
	return exists(ctx, ri.webhook(ctx))
}

func (ri injectorWebhook) Delete(ctx context.Context) error {
	return remove(ctx, ri.webhook(ctx))
}

func (ri injectorWebhook) Update(_ context.Context) error {
	// Noop
	return nil
}
