package resource

import (
	"context"

	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

type tmSvc int

var TrafficManagerSvc Instance = tmSvc(0)

func (ri tmSvc) service(ctx context.Context) *kates.Service {
	svc := new(kates.Service)
	svc.TypeMeta = kates.TypeMeta{
		Kind: "Service",
	}
	svc.ObjectMeta = kates.ObjectMeta{
		Namespace: getScope(ctx).namespace,
		Name:      install.ManagerAppName,
	}
	return svc
}

func (ri tmSvc) Create(ctx context.Context) error {
	svc := ri.service(ctx)
	svc.Spec = kates.ServiceSpec{
		Type:      "ClusterIP",
		ClusterIP: "None",
		Selector:  getScope(ctx).tmSelector,
		Ports: []kates.ServicePort{
			{
				Name: "api",
				Port: install.ManagerPortHTTP,
				TargetPort: kates.IntOrString{
					Type:   intstr.String,
					StrVal: "api",
				},
			},
		},
	}
	return create(ctx, svc)
}

func (ri tmSvc) Exists(ctx context.Context) (bool, error) {
	return exists(ctx, ri.service(ctx))
}

func (ri tmSvc) Delete(ctx context.Context) error {
	return remove(ctx, ri.service(ctx))
}

func (ri tmSvc) Update(_ context.Context) error {
	// Noop
	return nil
}
