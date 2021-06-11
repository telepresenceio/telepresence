package resource

import (
	"context"
	"time"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	cl "github.com/telepresenceio/telepresence/v2/pkg/client"
)

func waitForDeployApply(ctx context.Context, obj *kates.Deployment) error {
	ctx, cancel := cl.GetConfig(ctx).Timeouts.TimeoutContext(ctx, cl.TimeoutApply)
	defer cancel()

	origGeneration := obj.GetGeneration()
	key := &kates.Deployment{
		TypeMeta:   obj.TypeMeta,
		ObjectMeta: obj.ObjectMeta,
	}
	for {
		dtime.SleepWithContext(ctx, time.Second)
		if err := ctx.Err(); err != nil {
			return err
		}
		dep, err := find(ctx, key)
		if err != nil {
			return cl.CheckTimeout(ctx, err)
		}
		if dep != nil && deploymentUpdated(dep.(*kates.Deployment), origGeneration) {
			dlog.Debugf(ctx, "%s successfully applied", logName(dep))
			return nil
		}
	}
}

func deploymentUpdated(dep *kates.Deployment, origGeneration int64) bool {
	applied := dep.ObjectMeta.Generation >= origGeneration &&
		dep.Status.ObservedGeneration == dep.ObjectMeta.Generation &&
		(dep.Spec.Replicas == nil || dep.Status.UpdatedReplicas >= *dep.Spec.Replicas) &&
		dep.Status.UpdatedReplicas == dep.Status.Replicas &&
		dep.Status.AvailableReplicas == dep.Status.Replicas
	return applied
}
