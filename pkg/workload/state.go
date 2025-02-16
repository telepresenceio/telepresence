package workload

import (
	"slices"

	appsv1 "k8s.io/api/apps/v1"

	argorollouts "github.com/datawire/argo-rollouts-go-client/pkg/apis/rollouts/v1alpha1"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

type State int

const (
	StateUnknown State = iota
	StateProgressing
	StateAvailable
	StateFailure
)

func deploymentState(d *appsv1.Deployment) State {
	conds := d.Status.Conditions
	if slices.ContainsFunc(conds, func(c appsv1.DeploymentCondition) bool {
		return c.Status == "True" && c.Type == appsv1.DeploymentReplicaFailure
	}) {
		return StateFailure
	}
	if slices.ContainsFunc(conds, func(c appsv1.DeploymentCondition) bool {
		return c.Status == "True" && c.Type == appsv1.DeploymentAvailable
	}) {
		return StateAvailable
	}
	if slices.ContainsFunc(conds, func(c appsv1.DeploymentCondition) bool {
		return c.Status == "True" && c.Type == appsv1.DeploymentProgressing
	}) {
		return StateProgressing
	}
	return StateUnknown
}

func replicaSetState(d *appsv1.ReplicaSet) State {
	if slices.ContainsFunc(d.Status.Conditions, func(c appsv1.ReplicaSetCondition) bool {
		return c.Status == "True" && c.Type == appsv1.ReplicaSetReplicaFailure
	}) {
		return StateFailure
	}
	return StateAvailable
}

func statefulSetState(_ *appsv1.StatefulSet) State {
	return StateAvailable
}

func rolloutSetState(r *argorollouts.Rollout) State {
	conds := r.Status.Conditions
	if slices.ContainsFunc(conds, func(c argorollouts.RolloutCondition) bool {
		return c.Status == "True" && c.Type == argorollouts.RolloutReplicaFailure
	}) {
		return StateFailure
	}
	if slices.ContainsFunc(conds, func(c argorollouts.RolloutCondition) bool {
		return c.Status == "True" && c.Type == argorollouts.RolloutAvailable
	}) {
		return StateAvailable
	}
	if slices.ContainsFunc(conds, func(c argorollouts.RolloutCondition) bool {
		return c.Status == "True" && c.Type == argorollouts.RolloutProgressing
	}) {
		return StateProgressing
	}
	return StateUnknown
}

func (ws State) String() string {
	switch ws {
	case StateProgressing:
		return "Progressing"
	case StateAvailable:
		return "Available"
	case StateFailure:
		return "Failure"
	default:
		return "Unknown"
	}
}

func GetWorkloadState(wl k8sapi.Workload) State {
	if d, ok := k8sapi.DeploymentImpl(wl); ok {
		return deploymentState(d)
	}
	if r, ok := k8sapi.ReplicaSetImpl(wl); ok {
		return replicaSetState(r)
	}
	if s, ok := k8sapi.StatefulSetImpl(wl); ok {
		return statefulSetState(s)
	}
	if rt, ok := k8sapi.RolloutImpl(wl); ok {
		return rolloutSetState(rt)
	}
	return StateUnknown
}

func StateFromRPC(s manager.WorkloadInfo_State) State {
	switch s {
	case manager.WorkloadInfo_AVAILABLE:
		return StateAvailable
	case manager.WorkloadInfo_FAILURE:
		return StateFailure
	case manager.WorkloadInfo_PROGRESSING:
		return StateProgressing
	default:
		return StateUnknown
	}
}
