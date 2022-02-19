package k8sapi

import (
	"context"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	typedCore "k8s.io/client-go/kubernetes/typed/core/v1"
)

type Object interface {
	runtime.Object
	meta.Object
	GetAnnotations() map[string]string
	GetKind() string
	Delete(context.Context) error
	Refresh(context.Context) error
	Selector() (labels.Selector, error)
	Update(context.Context) error
	Patch(context.Context, types.PatchType, []byte, ...string) error
}

func GetService(c context.Context, name, namespace string) (Object, error) {
	d, err := services(c, namespace).Get(c, name, meta.GetOptions{})
	if err != nil {
		return nil, err
	}
	return &service{d}, nil
}

// Services returns all services found in the given Namespace
func Services(c context.Context, namespace string, labelSelector labels.Set) ([]Object, error) {
	ls, err := services(c, namespace).List(c, listOptions(labelSelector))
	if err != nil {
		return nil, err
	}
	is := ls.Items
	os := make([]Object, len(is))
	for i := range is {
		os[i] = Service(&is[i])
	}
	return os, nil
}

func Service(d *core.Service) Object {
	return &service{d}
}

// ServiceImpl casts the given Object as an *core.Service and returns
// it together with a status flag indicating whether the cast was possible
func ServiceImpl(o Object) (*core.Service, bool) {
	if s, ok := o.(*service); ok {
		return s.Service, true
	}
	return nil, false
}

func GetPod(c context.Context, name, namespace string) (Object, error) {
	d, err := pods(c, namespace).Get(c, name, meta.GetOptions{})
	if err != nil {
		return nil, err
	}
	return &pod{d}, nil
}

// Pods returns all pods found in the given Namespace
func Pods(c context.Context, namespace string, labelSelector labels.Set) ([]Object, error) {
	ls, err := pods(c, namespace).List(c, listOptions(labelSelector))
	if err != nil {
		return nil, err
	}
	is := ls.Items
	os := make([]Object, len(is))
	for i := range is {
		os[i] = Pod(&is[i])
	}
	return os, nil
}

func Pod(d *core.Pod) Object {
	return &pod{d}
}

// PodImpl casts the given Object as an *core.Pod and returns
// it together with a status flag indicating whether the cast was possible
func PodImpl(o Object) (*core.Pod, bool) {
	if s, ok := o.(*pod); ok {
		return s.Pod, true
	}
	return nil, false
}

type service struct {
	*core.Service
}

func services(c context.Context, namespace string) typedCore.ServiceInterface {
	return GetK8sInterface(c).CoreV1().Services(namespace)
}

func (o *service) ki(c context.Context) typedCore.ServiceInterface {
	return services(c, o.Namespace)
}

func (o *service) GetKind() string {
	return "Service"
}

func (o *service) Delete(c context.Context) error {
	return o.ki(c).Delete(c, o.Name, meta.DeleteOptions{})
}

func (o *service) Patch(c context.Context, pt types.PatchType, data []byte, subresources ...string) error {
	d, err := o.ki(c).Patch(c, o.Name, pt, data, meta.PatchOptions{}, subresources...)
	if err == nil {
		o.Service = d
	}
	return err
}

func (o *service) Refresh(c context.Context) error {
	d, err := o.ki(c).Get(c, o.Name, meta.GetOptions{})
	if err == nil {
		o.Service = d
	}
	return err
}

func (o *service) Selector() (labels.Selector, error) {
	if len(o.Spec.Selector) == 0 {
		return nil, nil
	}
	return labels.SelectorFromSet(o.Spec.Selector), nil
}

func (o *service) Update(c context.Context) error {
	d, err := o.ki(c).Update(c, o.Service, meta.UpdateOptions{})
	if err == nil {
		o.Service = d
	}
	return err
}

type pod struct {
	*core.Pod
}

func pods(c context.Context, namespace string) typedCore.PodInterface {
	return GetK8sInterface(c).CoreV1().Pods(namespace)
}

func (o *pod) ki(c context.Context) typedCore.PodInterface {
	return pods(c, o.Namespace)
}

func (o *pod) GetKind() string {
	return "Pod"
}

func (o *pod) Delete(c context.Context) error {
	return o.ki(c).Delete(c, o.Name, meta.DeleteOptions{})
}

func (o *pod) Patch(c context.Context, pt types.PatchType, data []byte, subresources ...string) error {
	d, err := o.ki(c).Patch(c, o.Name, pt, data, meta.PatchOptions{}, subresources...)
	if err == nil {
		o.Pod = d
	}
	return err
}

func (o *pod) Refresh(c context.Context) error {
	d, err := o.ki(c).Get(c, o.Name, meta.GetOptions{})
	if err == nil {
		o.Pod = d
	}
	return err
}

func (o *pod) Selector() (labels.Selector, error) {
	return nil, nil
}

func (o *pod) Update(c context.Context) error {
	d, err := o.ki(c).Update(c, o.Pod, meta.UpdateOptions{})
	if err == nil {
		o.Pod = d
	}
	return err
}
