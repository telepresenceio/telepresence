package mutator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	apps "k8s.io/api/apps/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func TestShouldWatchWorkload(t *testing.T) {
	controller := true
	notController := false
	enabledKinds := k8sapi.Kinds{
		k8sapi.DeploymentKind,
		k8sapi.ReplicaSetKind,
		k8sapi.RolloutKind,
	}

	tests := []struct {
		name            string
		kind            k8sapi.Kind
		ownerReferences []meta.OwnerReference
		enabledKinds    k8sapi.Kinds
		want            bool
	}{
		{
			name: "unowned ReplicaSet",
			kind: k8sapi.ReplicaSetKind,
			want: true,
		},
		{
			name: "non-controller ReplicaSet owner reference",
			kind: k8sapi.ReplicaSetKind,
			ownerReferences: []meta.OwnerReference{{
				Kind:       "CustomController",
				Controller: &notController,
			}},
			want: false,
		},
		{
			name: "custom-owned ReplicaSet",
			kind: k8sapi.ReplicaSetKind,
			ownerReferences: []meta.OwnerReference{{
				Kind:       "CustomController",
				Controller: &controller,
			}},
			want: true,
		},
		{
			name: "enabled Deployment-owned ReplicaSet",
			kind: k8sapi.ReplicaSetKind,
			ownerReferences: []meta.OwnerReference{{
				Kind:       string(k8sapi.DeploymentKind),
				Controller: &controller,
			}},
			enabledKinds: enabledKinds,
			want:         false,
		},
		{
			name: "enabled Rollout-owned ReplicaSet",
			kind: k8sapi.ReplicaSetKind,
			ownerReferences: []meta.OwnerReference{{
				Kind:       string(k8sapi.RolloutKind),
				Controller: &controller,
			}},
			enabledKinds: enabledKinds,
			want:         false,
		},
		{
			name: "disabled Deployment-owned ReplicaSet",
			kind: k8sapi.ReplicaSetKind,
			ownerReferences: []meta.OwnerReference{{
				Kind:       string(k8sapi.DeploymentKind),
				Controller: &controller,
			}},
			enabledKinds: k8sapi.Kinds{k8sapi.ReplicaSetKind},
			want:         true,
		},
		{
			name: "custom-owned Deployment",
			kind: k8sapi.DeploymentKind,
			ownerReferences: []meta.OwnerReference{{
				Kind:       "CustomController",
				Controller: &controller,
			}},
			want: false,
		},
		{
			name: "custom-owned StatefulSet",
			kind: k8sapi.StatefulSetKind,
			ownerReferences: []meta.OwnerReference{{
				Kind:       "CustomController",
				Controller: &controller,
			}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wl k8sapi.Workload
			switch tt.kind {
			case k8sapi.DeploymentKind:
				wl = k8sapi.Deployment(&apps.Deployment{
					ObjectMeta: meta.ObjectMeta{OwnerReferences: tt.ownerReferences},
				})
			case k8sapi.StatefulSetKind:
				wl = k8sapi.StatefulSet(&apps.StatefulSet{
					ObjectMeta: meta.ObjectMeta{OwnerReferences: tt.ownerReferences},
				})
			default:
				wl = k8sapi.ReplicaSet(&apps.ReplicaSet{
					ObjectMeta: meta.ObjectMeta{OwnerReferences: tt.ownerReferences},
				})
			}
			assert.Equal(t, tt.want, shouldWatchWorkload(wl, tt.enabledKinds))
		})
	}
}
