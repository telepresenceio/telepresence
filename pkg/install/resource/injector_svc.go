package resource

import (
	"context"

	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

type injectorSvc int

var AgentInjectorSvc Instance = injectorSvc(0)

func (ri injectorSvc) service(ctx context.Context) *kates.Service {
	svc := new(kates.Service)
	svc.TypeMeta = kates.TypeMeta{
		Kind: "Service",
	}
	svc.ObjectMeta = kates.ObjectMeta{
		Namespace: getScope(ctx).namespace,
		Name:      install.AgentInjectorName,
	}
	return svc
}

func (ri injectorSvc) Create(ctx context.Context) error {
	svc := ri.service(ctx)
	svc.Spec = kates.ServiceSpec{
		Type:     "ClusterIP",
		Selector: getScope(ctx).tmSelector,
		Ports: []kates.ServicePort{
			{
				Name: "https",
				Port: 443,
				TargetPort: kates.IntOrString{
					Type:   intstr.String,
					StrVal: "https",
				},
			},
		},
	}
	return create(ctx, svc)
}

func (ri injectorSvc) Exists(ctx context.Context) (bool, error) {
	return exists(ctx, ri.service(ctx))
}

func (ri injectorSvc) Delete(ctx context.Context) error {
	return remove(ctx, ri.service(ctx))
}

func (ri injectorSvc) Update(_ context.Context) error {
	// Noop
	return nil
}
