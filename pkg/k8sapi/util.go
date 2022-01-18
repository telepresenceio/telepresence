package k8sapi

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	admreg "k8s.io/api/admissionregistration/v1"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	rbac "k8s.io/api/rbac/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/datawire/dlib/dlog"
)

// Get returns a fresh version of the given object.
func Get(c context.Context, o runtime.Object) (runtime.Object, error) {
	ki := GetK8sInterface(c)
	opts := meta.GetOptions{}
	switch o := o.(type) {
	case *core.Service:
		return ki.CoreV1().Services(o.Namespace).Get(c, o.Name, opts)
	case *core.Pod:
		return ki.CoreV1().Pods(o.Namespace).Get(c, o.Name, opts)
	case *core.Secret:
		return ki.CoreV1().Secrets(o.Namespace).Get(c, o.Name, opts)
	case *core.ServiceAccount:
		return ki.CoreV1().ServiceAccounts(o.Namespace).Get(c, o.Name, opts)
	case *apps.Deployment:
		return ki.AppsV1().Deployments(o.Namespace).Get(c, o.Name, opts)
	case *apps.ReplicaSet:
		return ki.AppsV1().ReplicaSets(o.Namespace).Get(c, o.Name, opts)
	case *apps.StatefulSet:
		return ki.AppsV1().StatefulSets(o.Namespace).Get(c, o.Name, opts)
	case *apps.DaemonSet:
		return ki.AppsV1().DaemonSets(o.Namespace).Get(c, o.Name, opts)
	case *rbac.ClusterRole:
		return ki.RbacV1().ClusterRoles().Get(c, o.Name, opts)
	case *rbac.ClusterRoleBinding:
		return ki.RbacV1().ClusterRoleBindings().Get(c, o.Name, opts)
	case *rbac.Role:
		return ki.RbacV1().Roles(o.Namespace).Get(c, o.Name, opts)
	case *rbac.RoleBinding:
		return ki.RbacV1().RoleBindings(o.Namespace).Get(c, o.Name, opts)
	case *admreg.MutatingWebhookConfiguration:
		return ki.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(c, o.Name, opts)
	default:
		return nil, ObjErrorf(o, "unsupported object type %T", o)
	}
}

// Delete deletes an object in the cluster.
func Delete(c context.Context, o runtime.Object) error {
	ki := GetK8sInterface(c)
	opts := meta.DeleteOptions{}
	switch o := o.(type) {
	case *core.Service:
		return ki.CoreV1().Services(o.Namespace).Delete(c, o.Name, opts)
	case *core.Pod:
		return ki.CoreV1().Pods(o.Namespace).Delete(c, o.Name, opts)
	case *core.Secret:
		return ki.CoreV1().Secrets(o.Namespace).Delete(c, o.Name, opts)
	case *core.ServiceAccount:
		return ki.CoreV1().ServiceAccounts(o.Namespace).Delete(c, o.Name, opts)
	case *apps.Deployment:
		return ki.AppsV1().Deployments(o.Namespace).Delete(c, o.Name, opts)
	case *apps.ReplicaSet:
		return ki.AppsV1().ReplicaSets(o.Namespace).Delete(c, o.Name, opts)
	case *apps.StatefulSet:
		return ki.AppsV1().StatefulSets(o.Namespace).Delete(c, o.Name, opts)
	case *apps.DaemonSet:
		return ki.AppsV1().DaemonSets(o.Namespace).Delete(c, o.Name, opts)
	case *rbac.ClusterRole:
		return ki.RbacV1().ClusterRoles().Delete(c, o.Name, opts)
	case *rbac.ClusterRoleBinding:
		return ki.RbacV1().ClusterRoleBindings().Delete(c, o.Name, opts)
	case *rbac.Role:
		return ki.RbacV1().Roles(o.Namespace).Delete(c, o.Name, opts)
	case *rbac.RoleBinding:
		return ki.RbacV1().RoleBindings(o.Namespace).Delete(c, o.Name, opts)
	case *admreg.MutatingWebhookConfiguration:
		return ki.AdmissionregistrationV1().MutatingWebhookConfigurations().Delete(c, o.Name, opts)
	default:
		return ObjErrorf(o, "unsupported object type %T", o)
	}
}

// Patch patches an object in the cluster.
func Patch(c context.Context, o runtime.Object, pt types.PatchType, data []byte, subresources ...string) (runtime.Object, error) {
	ki := GetK8sInterface(c)
	opts := meta.PatchOptions{}
	switch o := o.(type) {
	case *core.Service:
		return ki.CoreV1().Services(o.Namespace).Patch(c, o.Name, pt, data, opts, subresources...)
	case *core.Pod:
		return ki.CoreV1().Pods(o.Namespace).Patch(c, o.Name, pt, data, opts, subresources...)
	case *apps.Deployment:
		return ki.AppsV1().Deployments(o.Namespace).Patch(c, o.Name, pt, data, opts, subresources...)
	case *apps.ReplicaSet:
		return ki.AppsV1().ReplicaSets(o.Namespace).Patch(c, o.Name, pt, data, opts, subresources...)
	case *apps.StatefulSet:
		return ki.AppsV1().StatefulSets(o.Namespace).Patch(c, o.Name, pt, data, opts, subresources...)
	case *apps.DaemonSet:
		return ki.AppsV1().DaemonSets(o.Namespace).Patch(c, o.Name, pt, data, opts, subresources...)
	default:
		return nil, ObjErrorf(o, "unsupported object type %T", o)
	}
}

// Update updates an object in the cluster.
func Update(c context.Context, o runtime.Object) (runtime.Object, error) {
	ki := GetK8sInterface(c)
	opts := meta.UpdateOptions{}
	switch o := o.(type) {
	case *core.Service:
		return ki.CoreV1().Services(o.Namespace).Update(c, o, opts)
	case *core.Pod:
		return ki.CoreV1().Pods(o.Namespace).Update(c, o, opts)
	case *core.Secret:
		return ki.CoreV1().Secrets(o.Namespace).Update(c, o, opts)
	case *core.ServiceAccount:
		return ki.CoreV1().ServiceAccounts(o.Namespace).Update(c, o, opts)
	case *apps.Deployment:
		return ki.AppsV1().Deployments(o.Namespace).Update(c, o, opts)
	case *apps.ReplicaSet:
		return ki.AppsV1().ReplicaSets(o.Namespace).Update(c, o, opts)
	case *apps.StatefulSet:
		return ki.AppsV1().StatefulSets(o.Namespace).Update(c, o, opts)
	case *apps.DaemonSet:
		return ki.AppsV1().DaemonSets(o.Namespace).Update(c, o, opts)
	case *rbac.ClusterRole:
		return ki.RbacV1().ClusterRoles().Update(c, o, opts)
	case *rbac.ClusterRoleBinding:
		return ki.RbacV1().ClusterRoleBindings().Update(c, o, opts)
	case *rbac.Role:
		return ki.RbacV1().Roles(o.Namespace).Update(c, o, opts)
	case *rbac.RoleBinding:
		return ki.RbacV1().RoleBindings(o.Namespace).Update(c, o, opts)
	case *admreg.MutatingWebhookConfiguration:
		return ki.AdmissionregistrationV1().MutatingWebhookConfigurations().Update(c, o, opts)
	default:
		return nil, ObjErrorf(o, "unsupported object type %T", o)
	}
}

func WithK8sInterface(ctx context.Context, ki kubernetes.Interface) context.Context {
	return context.WithValue(ctx, kiKey{}, ki)
}

func GetK8sInterface(ctx context.Context) kubernetes.Interface {
	ki, ok := ctx.Value(kiKey{}).(kubernetes.Interface)
	if !ok {
		return nil
	}
	return ki
}

type kiKey struct{}

func GetPodTemplateFromObject(o runtime.Object) (*core.PodTemplateSpec, error) {
	var tplSpec *core.PodTemplateSpec
	switch o := o.(type) {
	case *apps.ReplicaSet:
		tplSpec = &o.Spec.Template
	case *apps.Deployment:
		tplSpec = &o.Spec.Template
	case *apps.StatefulSet:
		tplSpec = &o.Spec.Template
	case *apps.DaemonSet:
		tplSpec = &o.Spec.Template
	default:
		return nil, ObjErrorf(o, "unsupported workload %T", o)
	}
	return tplSpec, nil
}

// GetPort finds a port with the given name and returns it.
func GetPort(cn *core.Container, portName string) (*core.ContainerPort, error) {
	ports := cn.Ports
	for pn := range ports {
		p := &ports[pn]
		if p.Name == portName {
			return p, nil
		}
	}
	return nil, fmt.Errorf("unable to locate port %q in container %q", portName, cn.Name)
}

// GetAppProto determines the application protocol of the given ServicePort. The given AppProtocolStrategy
// used if the port's appProtocol attribute is unset.
func GetAppProto(ctx context.Context, aps AppProtocolStrategy, p *core.ServicePort) string {
	if p.AppProtocol != nil {
		appProto := *p.AppProtocol
		if appProto != "" {
			dlog.Debugf(ctx, "Using application protocol %q from service appProtocol field", appProto)
			return appProto
		}
	}

	switch aps {
	case Http:
		return "http"
	case Http2:
		return "http2"
	case PortName:
		if p.Name == "" {
			dlog.Debug(ctx, "Unable to derive application protocol from unnamed service port with no appProtocol field")
			break
		}
		pn := p.Name
		if dashPos := strings.IndexByte(pn, '-'); dashPos > 0 {
			pn = pn[:dashPos]
		}
		var appProto string
		switch strings.ToLower(pn) {
		case "http", "https", "grpc", "http2":
			appProto = pn
		case "h2c": // h2c is cleartext HTTP/2
			appProto = "http2"
		case "tls", "h2": // same as https in this context and h2 is HTTP/2 with TLS
			appProto = "https"
		}
		if appProto != "" {
			dlog.Debugf(ctx, "Using application protocol %q derived from port name %q", appProto, p.Name)
			return appProto
		}
		dlog.Debugf(ctx, "Unable to derive application protocol from port name %q", p.Name)
	}
	return ""
}

func ObjErrorf(o runtime.Object, format string, args ...interface{}) error {
	return fmt.Errorf("%s name=%q namespace=%q: %w",
		GetKind(o), GetName(o), GetNamespace(o),
		fmt.Errorf(format, args...))
}

func GetAnnotations(o runtime.Object) map[string]string {
	return o.(meta.ObjectMetaAccessor).GetObjectMeta().GetAnnotations()
}

func GetGeneration(o runtime.Object) int64 {
	return o.(meta.ObjectMetaAccessor).GetObjectMeta().GetGeneration()
}

func GetKind(o runtime.Object) string {
	// Does this look weird? It is weird, but the TypeMeta isn't added by the standard
	// Kubernetes Get/List methods. It's OK for the Kind attribute, since we can trust
	// the name of the actual type
	// See https://github.com/kubernetes/kubernetes/issues/3030
	return reflect.ValueOf(o).Elem().Type().Name()
}

func GetName(o runtime.Object) string {
	return o.(meta.ObjectMetaAccessor).GetObjectMeta().GetName()
}

func GetNamespace(o runtime.Object) string {
	return o.(meta.ObjectMetaAccessor).GetObjectMeta().GetNamespace()
}
