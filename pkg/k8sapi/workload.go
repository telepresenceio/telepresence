package k8sapi

import (
	"context"
	"fmt"

	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	errors2 "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	typedApps "k8s.io/client-go/kubernetes/typed/apps/v1"
)

type Workload interface {
	Object
	GetPodTemplate() *core.PodTemplateSpec
	Replicas() int
	Updated(int64) bool
}

// GetWorkload returns a workload for the given name, namespace, and workloadKind. The workloadKind
// is optional. A search is performed in the following order if it is empty:
//
//   1. Deployments
//   2. ReplicaSets
//   3. StatefulSets
//
// The first match is returned.
func GetWorkload(c context.Context, name, namespace, workloadKind string) (obj Workload, err error) {
	switch workloadKind {
	case "Deployment":
		obj, err = GetDeployment(c, name, namespace)
	case "ReplicaSet":
		obj, err = GetReplicaSet(c, name, namespace)
	case "StatefulSet":
		obj, err = GetStatefulSet(c, name, namespace)
	case "":
		for _, wk := range []string{"Deployment", "ReplicaSet", "StatefulSet"} {
			if obj, err = GetWorkload(c, name, namespace, wk); err == nil {
				return obj, nil
			}
			if !errors2.IsNotFound(err) {
				return nil, err
			}
		}
		err = errors2.NewNotFound(core.Resource("workload"), name+"."+namespace)
	default:
		return nil, fmt.Errorf("unsupported workload kind: %q", workloadKind)
	}
	return obj, err
}

func WrapWorkload(workload runtime.Object) (Workload, error) {
	switch workload := workload.(type) {
	case *apps.Deployment:
		return Deployment(workload), nil
	case *apps.ReplicaSet:
		return ReplicaSet(workload), nil
	case *apps.StatefulSet:
		return StatefulSet(workload), nil
	default:
		return nil, fmt.Errorf("unsupported workload type %T", workload)
	}
}

func GetDeployment(c context.Context, name, namespace string) (Workload, error) {
	d, err := deployments(c, namespace).Get(c, name, meta.GetOptions{})
	if err != nil {
		return nil, err
	}
	return &deployment{d}, nil
}

// Deployments returns all deployments found in the given Namespace
func Deployments(c context.Context, namespace string, labelSelector labels.Set) ([]Workload, error) {
	ls, err := deployments(c, namespace).List(c, listOptions(labelSelector))
	if err != nil {
		return nil, err
	}
	is := ls.Items
	os := make([]Workload, len(is))
	for i := range is {
		os[i] = Deployment(&is[i])
	}
	return os, nil
}

func Deployment(d *apps.Deployment) Workload {
	return &deployment{d}
}

// DeploymentImpl casts the given Object as an *apps.Deployment and returns
// it together with a status flag indicating whether the cast was possible
func DeploymentImpl(o Object) (*apps.Deployment, bool) {
	if s, ok := o.(*deployment); ok {
		return s.Deployment, true
	}
	return nil, false
}

func GetReplicaSet(c context.Context, name, namespace string) (Workload, error) {
	d, err := replicaSets(c, namespace).Get(c, name, meta.GetOptions{})
	if err != nil {
		return nil, err
	}
	return &replicaSet{d}, nil
}

// ReplicaSets returns all replica sets found in the given Namespace
func ReplicaSets(c context.Context, namespace string, labelSelector labels.Set) ([]Workload, error) {
	ls, err := replicaSets(c, namespace).List(c, listOptions(labelSelector))
	if err != nil {
		return nil, err
	}
	is := ls.Items
	os := make([]Workload, len(is))
	for i := range is {
		os[i] = ReplicaSet(&is[i])
	}
	return os, nil
}

func ReplicaSet(d *apps.ReplicaSet) Workload {
	return &replicaSet{d}
}

// ReplicaSetImpl casts the given Object as an *apps.ReplicaSet and returns
// it together with a status flag indicating whether the cast was possible
func ReplicaSetImpl(o Object) (*apps.ReplicaSet, bool) {
	if s, ok := o.(*replicaSet); ok {
		return s.ReplicaSet, true
	}
	return nil, false
}

func GetStatefulSet(c context.Context, name, namespace string) (Workload, error) {
	d, err := statefulSets(c, namespace).Get(c, name, meta.GetOptions{})
	if err != nil {
		return nil, err
	}
	return &statefulSet{d}, nil
}

// StatefulSets returns all stateful sets found in the given Namespace
func StatefulSets(c context.Context, namespace string, labelSelector labels.Set) ([]Workload, error) {
	ls, err := statefulSets(c, namespace).List(c, listOptions(labelSelector))
	if err != nil {
		return nil, err
	}
	is := ls.Items
	os := make([]Workload, len(is))
	for i := range is {
		os[i] = StatefulSet(&is[i])
	}
	return os, nil
}

func StatefulSet(d *apps.StatefulSet) Workload {
	return &statefulSet{d}
}

// StatefulSetImpl casts the given Object as an *apps.StatefulSet and returns
// it together with a status flag indicating whether the cast was possible
func StatefulSetImpl(o Object) (*apps.StatefulSet, bool) {
	if s, ok := o.(*statefulSet); ok {
		return s.StatefulSet, true
	}
	return nil, false
}

type deployment struct {
	*apps.Deployment
}

func deployments(c context.Context, namespace string) typedApps.DeploymentInterface {
	return GetK8sInterface(c).AppsV1().Deployments(namespace)
}

func (o *deployment) ki(c context.Context) typedApps.DeploymentInterface {
	return deployments(c, o.Namespace)
}

func (o *deployment) GetKind() string {
	return "Deployment"
}

func (o *deployment) Delete(c context.Context) error {
	return o.ki(c).Delete(c, o.Name, meta.DeleteOptions{})
}

func (o *deployment) GetPodTemplate() *core.PodTemplateSpec {
	return &o.Spec.Template
}

func (o *deployment) Patch(c context.Context, pt types.PatchType, data []byte, subresources ...string) error {
	d, err := o.ki(c).Patch(c, o.Name, pt, data, meta.PatchOptions{}, subresources...)
	if err == nil {
		o.Deployment = d
	}
	return err
}

func (o *deployment) Refresh(c context.Context) error {
	d, err := o.ki(c).Get(c, o.Name, meta.GetOptions{})
	if err == nil {
		o.Deployment = d
	}
	return err
}

func (o *deployment) Replicas() int {
	return int(o.Status.Replicas)
}

func (o *deployment) Selector() (labels.Selector, error) {
	return meta.LabelSelectorAsSelector(o.Spec.Selector)
}

func (o *deployment) Update(c context.Context) error {
	d, err := o.ki(c).Update(c, o.Deployment, meta.UpdateOptions{})
	if err == nil {
		o.Deployment = d
	}
	return err
}

func (o *deployment) Updated(origGeneration int64) bool {
	applied := o.ObjectMeta.Generation >= origGeneration &&
		o.Status.ObservedGeneration == o.ObjectMeta.Generation &&
		(o.Spec.Replicas == nil || o.Status.UpdatedReplicas >= *o.Spec.Replicas) &&
		o.Status.UpdatedReplicas == o.Status.Replicas &&
		o.Status.AvailableReplicas == o.Status.Replicas
	return applied
}

type replicaSet struct {
	*apps.ReplicaSet
}

func replicaSets(c context.Context, namespace string) typedApps.ReplicaSetInterface {
	return GetK8sInterface(c).AppsV1().ReplicaSets(namespace)
}

func (o *replicaSet) ki(c context.Context) typedApps.ReplicaSetInterface {
	return replicaSets(c, o.Namespace)
}

func (o *replicaSet) GetKind() string {
	return "ReplicaSet"
}

func (o *replicaSet) Delete(c context.Context) error {
	return o.ki(c).Delete(c, o.Name, meta.DeleteOptions{})
}

func (o *replicaSet) GetPodTemplate() *core.PodTemplateSpec {
	return &o.Spec.Template
}

func (o *replicaSet) Patch(c context.Context, pt types.PatchType, data []byte, subresources ...string) error {
	d, err := o.ki(c).Patch(c, o.Name, pt, data, meta.PatchOptions{}, subresources...)
	if err == nil {
		o.ReplicaSet = d
	}
	return err
}

func (o *replicaSet) Refresh(c context.Context) error {
	d, err := o.ki(c).Get(c, o.Name, meta.GetOptions{})
	if err == nil {
		o.ReplicaSet = d
	}
	return err
}

func (o *replicaSet) Replicas() int {
	return int(o.Status.Replicas)
}

func (o *replicaSet) Selector() (labels.Selector, error) {
	return meta.LabelSelectorAsSelector(o.Spec.Selector)
}

func (o *replicaSet) Update(c context.Context) error {
	d, err := o.ki(c).Update(c, o.ReplicaSet, meta.UpdateOptions{})
	if err == nil {
		o.ReplicaSet = d
	}
	return err
}

func (o *replicaSet) Updated(origGeneration int64) bool {
	applied := o.ObjectMeta.Generation >= origGeneration &&
		o.Status.ObservedGeneration == o.ObjectMeta.Generation &&
		(o.Spec.Replicas == nil || o.Status.Replicas >= *o.Spec.Replicas) &&
		o.Status.FullyLabeledReplicas == o.Status.Replicas &&
		o.Status.AvailableReplicas == o.Status.Replicas
	return applied
}

type statefulSet struct {
	*apps.StatefulSet
}

func statefulSets(c context.Context, namespace string) typedApps.StatefulSetInterface {
	return GetK8sInterface(c).AppsV1().StatefulSets(namespace)
}

func (o *statefulSet) ki(c context.Context) typedApps.StatefulSetInterface {
	return statefulSets(c, o.Namespace)
}

func (o *statefulSet) GetKind() string {
	return "StatefulSet"
}

func (o *statefulSet) Delete(c context.Context) error {
	return o.ki(c).Delete(c, o.Name, meta.DeleteOptions{})
}

func (o *statefulSet) GetPodTemplate() *core.PodTemplateSpec {
	return &o.Spec.Template
}

func (o *statefulSet) Patch(c context.Context, pt types.PatchType, data []byte, subresources ...string) error {
	d, err := o.ki(c).Patch(c, o.Name, pt, data, meta.PatchOptions{}, subresources...)
	if err == nil {
		o.StatefulSet = d
	}
	return err
}

func (o *statefulSet) Refresh(c context.Context) error {
	d, err := o.ki(c).Get(c, o.Name, meta.GetOptions{})
	if err == nil {
		o.StatefulSet = d
	}
	return err
}

func (o *statefulSet) Replicas() int {
	return int(o.Status.Replicas)
}

func (o *statefulSet) Selector() (labels.Selector, error) {
	return meta.LabelSelectorAsSelector(o.Spec.Selector)
}

func (o *statefulSet) Update(c context.Context) error {
	d, err := o.ki(c).Update(c, o.StatefulSet, meta.UpdateOptions{})
	if err == nil {
		o.StatefulSet = d
	}
	return err
}

func (o *statefulSet) Updated(origGeneration int64) bool {
	applied := o.ObjectMeta.Generation >= origGeneration &&
		o.Status.ObservedGeneration == o.ObjectMeta.Generation &&
		(o.Spec.Replicas == nil || o.Status.UpdatedReplicas >= *o.Spec.Replicas) &&
		o.Status.UpdatedReplicas == o.Status.Replicas &&
		o.Status.CurrentReplicas == o.Status.Replicas
	return applied
}
