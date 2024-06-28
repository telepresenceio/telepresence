package mutator

import (
	appsv1 "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"

	"github.com/datawire/k8sapi/pkg/k8sapi"
)

type WorkloadState int

const (
	WorkloadStateUnknown WorkloadState = iota
	WorkloadStateProgressing
	WorkloadStateAvailable
	WorkloadStateFailure
)

func deploymentState(d *appsv1.Deployment) WorkloadState {
	for _, c := range d.Status.Conditions {
		switch c.Type {
		case appsv1.DeploymentProgressing:
			if c.Status == core.ConditionTrue {
				return WorkloadStateProgressing
			}
		case appsv1.DeploymentAvailable:
			if c.Status == core.ConditionTrue {
				return WorkloadStateAvailable
			}
		case appsv1.DeploymentReplicaFailure:
			if c.Status == core.ConditionTrue {
				return WorkloadStateFailure
			}
		}
	}
	return WorkloadStateUnknown
}

func replicaSetState(d *appsv1.ReplicaSet) WorkloadState {
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.ReplicaSetReplicaFailure && c.Status == core.ConditionTrue {
			return WorkloadStateFailure
		}
	}
	return WorkloadStateAvailable
}

func statefulSetState(d *appsv1.StatefulSet) WorkloadState {
	return WorkloadStateAvailable
}

func workloadState(wl k8sapi.Workload) WorkloadState {
	if d, ok := k8sapi.DeploymentImpl(wl); ok {
		return deploymentState(d)
	}
	if r, ok := k8sapi.ReplicaSetImpl(wl); ok {
		return replicaSetState(r)
	}
	if s, ok := k8sapi.StatefulSetImpl(wl); ok {
		return statefulSetState(s)
	}
	return WorkloadStateUnknown
}
