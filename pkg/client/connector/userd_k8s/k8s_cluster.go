package userd_k8s

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/actions"
)

type nameMeta struct {
	Name string `json:"name"`
}

type objName struct {
	nameMeta `json:"metadata"`
}

type Callbacks struct {
	SetDNSSearchPath func(ctx context.Context, in *daemon.Paths, opts ...grpc.CallOption) (*empty.Empty, error)
}

// k8sCluster is a Kubernetes cluster reference
type Cluster struct {
	*Config
	mappedNamespaces []string

	// Main
	client    *kates.Client
	callbacks Callbacks

	lastNamespaces []string

	// Currently intercepted namespaces by remote intercepts
	interceptedNamespaces map[string]struct{}

	// Currently intercepted namespaces by local intercepts
	localInterceptedNamespaces map[string]struct{}

	accLock         sync.Mutex
	accWait         chan struct{}
	LocalIntercepts map[string]string

	// Current Namespace snapshot, get set by acc.Update().
	curSnapshot struct {
		Namespaces []*objName
	}
}

func (kc *Cluster) ActualNamespace(namespace string) string {
	if namespace == "" {
		namespace = kc.Namespace
	}
	if !kc.namespaceExists(namespace) {
		namespace = ""
	}
	return namespace
}

// check uses a non-caching DiscoveryClientConfig to retrieve the server version
func (kc *Cluster) check(c context.Context) error {
	// The discover client is using context.TODO() so the timeout specified in our
	// context has no effect.
	errCh := make(chan error)
	go func() {
		dc, err := discovery.NewDiscoveryClientForConfig(kc.config)
		if err != nil {
			errCh <- err
			return
		}
		info, err := dc.ServerVersion()
		if err != nil {
			errCh <- err
			return
		}
		dlog.Infof(c, "Server version %s", info.GitVersion)
		close(errCh)
	}()

	select {
	case <-c.Done():
	case err := <-errCh:
		if err == nil {
			return nil
		}
		if c.Err() == nil {
			return fmt.Errorf("initial cluster check failed: %w", client.RunError(err))
		}
	}
	return c.Err()
}

// kindNames returns the names of all objects of a specified Kind in a given Namespace
func (kc *Cluster) kindNames(c context.Context, kind, namespace string) ([]string, error) {
	var objNames []objName
	if err := kc.client.List(c, kates.Query{Kind: kind, Namespace: namespace}, &objNames); err != nil {
		return nil, err
	}
	names := make([]string, len(objNames))
	for i, n := range objNames {
		names[i] = n.Name
	}
	return names, nil
}

// DeploymentNames returns the names of all deployments found in the given Namespace
func (kc *Cluster) DeploymentNames(c context.Context, namespace string) ([]string, error) {
	return kc.kindNames(c, "Deployment", namespace)
}

// ReplicaSetNames returns the names of all replica sets found in the given Namespace
func (kc *Cluster) ReplicaSetNames(c context.Context, namespace string) ([]string, error) {
	return kc.kindNames(c, "ReplicaSet", namespace)
}

// StatefulSetNames returns the names of all replica sets found in the given Namespace
func (kc *Cluster) StatefulSetNames(c context.Context, namespace string) ([]string, error) {
	return kc.kindNames(c, "StatefulSet", namespace)
}

// PodNames returns the names of all replica sets found in the given Namespace
func (kc *Cluster) PodNames(c context.Context, namespace string) ([]string, error) {
	return kc.kindNames(c, "Pod", namespace)
}

// FindDeployment returns a deployment with the given name in the given namespace or nil
// if no such deployment could be found.
func (kc *Cluster) FindDeployment(c context.Context, namespace, name string) (*kates.Deployment, error) {
	dep := &kates.Deployment{
		TypeMeta:   kates.TypeMeta{Kind: "Deployment"},
		ObjectMeta: kates.ObjectMeta{Name: name, Namespace: namespace},
	}
	if err := kc.client.Get(c, dep, dep); err != nil {
		return nil, err
	}
	return dep, nil
}

// FindStatefulSet returns a statefulSet with the given name in the given namespace or nil
// if no such statefulSet could be found.
func (kc *Cluster) FindStatefulSet(c context.Context, namespace, name string) (*kates.StatefulSet, error) {
	statefulSet := &kates.StatefulSet{
		TypeMeta:   kates.TypeMeta{Kind: "StatefulSet"},
		ObjectMeta: kates.ObjectMeta{Name: name, Namespace: namespace},
	}
	if err := kc.client.Get(c, statefulSet, statefulSet); err != nil {
		return nil, err
	}
	return statefulSet, nil
}

// FindReplicaSet returns a replica set with the given name in the given namespace or nil
// if no such replica set could be found.
func (kc *Cluster) FindReplicaSet(c context.Context, namespace, name string) (*kates.ReplicaSet, error) {
	rs := &kates.ReplicaSet{
		TypeMeta:   kates.TypeMeta{Kind: "ReplicaSet"},
		ObjectMeta: kates.ObjectMeta{Name: name, Namespace: namespace},
	}
	if err := kc.client.Get(c, rs, rs); err != nil {
		return nil, err
	}
	return rs, nil
}

// FindAgain returns a fresh version of the given object.
func (kc *Cluster) FindAgain(c context.Context, obj kates.Object) (kates.Object, error) {
	if err := kc.client.Get(c, obj, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// FindPod returns a pod with the given name in the given namespace or nil
// if no such replica set could be found.
func (kc *Cluster) FindPod(c context.Context, namespace, name string) (*kates.Pod, error) {
	pod := &kates.Pod{
		TypeMeta:   kates.TypeMeta{Kind: "Pod"},
		ObjectMeta: kates.ObjectMeta{Name: name, Namespace: namespace},
	}
	if err := kc.client.Get(c, pod, pod); err != nil {
		return nil, err
	}
	return pod, nil
}

// FindWorkload returns a workload for the given name and namespace. We
// search in a specific order based on how we prefer workload objects:
// 1. Deployments
// 2. ReplicaSets
// 3. StatefulSets
// And return the kind as soon as we find one that matches
func (kc *Cluster) FindWorkload(c context.Context, namespace, name string) (kates.Object, error) {
	type workLoad struct {
		kind string
		obj  kates.Object
	}
	for _, wl := range []workLoad{{"Deployment", &kates.Deployment{}}, {"ReplicaSet", &kates.ReplicaSet{}}, {"StatefulSet", &kates.StatefulSet{}}} {
		wl.obj.(schema.ObjectKind).SetGroupVersionKind(schema.GroupVersionKind{Kind: wl.kind})
		wl.obj.SetName(name)
		wl.obj.SetNamespace(namespace)
		if err := kc.client.Get(c, wl.obj, wl.obj); err != nil {
			if kates.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		return wl.obj, nil
	}
	return nil, errors.NewNotFound(corev1.Resource("workload"), name+"."+namespace)
}

// FindSvc finds a service with the given name in the given Namespace and returns
// either a copy of that service or nil if no such service could be found.
func (kc *Cluster) FindSvc(c context.Context, namespace, name string) (*kates.Service, error) {
	rs := &kates.Service{
		TypeMeta:   kates.TypeMeta{Kind: "Service"},
		ObjectMeta: kates.ObjectMeta{Name: name, Namespace: namespace},
	}
	if err := kc.client.Get(c, rs, rs); err != nil {
		return nil, err
	}
	return rs, nil
}

// findAllSvc finds services with the given service type in all namespaces of the cluster returns
// a slice containing a copy of those services.
func (kc *Cluster) findAllSvcByType(c context.Context, svcType corev1.ServiceType) ([]*kates.Service, error) {
	// NOTE: This is expensive in terms of bandwidth on a large cluster. We currently only use this
	// to retrieve ingress info and that task could be moved to the traffic-manager instead.
	var svcs []*kates.Service
	if err := kc.client.List(c, kates.Query{Kind: "Service"}, &svcs); err != nil {
		return nil, err
	}
	var typedSvcs []*kates.Service
	for _, svc := range svcs {
		if svc.Spec.Type == svcType {
			typedSvcs = append(typedSvcs, svc)
			break
		}
	}
	return typedSvcs, nil
}

func (kc *Cluster) namespaceExists(namespace string) (exists bool) {
	kc.accLock.Lock()
	for _, n := range kc.lastNamespaces {
		if n == namespace {
			exists = true
			break
		}
	}
	kc.accLock.Unlock()
	return exists
}

func NewCluster(c context.Context, kubeFlags *Config, mappedNamespaces []string, callbacks Callbacks) (*Cluster, error) {
	// TODO: Add constructor to kates that takes an additional restConfig argument to prevent that kates recreates it.
	kc, err := kates.NewClientFromConfigFlags(kubeFlags.ConfigFlags)
	if err != nil {
		return nil, client.CheckTimeout(c, fmt.Errorf("k8s client create failed: %w", err))
	}

	ret := &Cluster{
		Config:           kubeFlags,
		mappedNamespaces: mappedNamespaces,
		client:           kc,
		callbacks:        callbacks,
		LocalIntercepts:  map[string]string{},
		accWait:          make(chan struct{}),
	}

	if err := ret.check(c); err != nil {
		return nil, err
	}

	dlog.Infof(c, "Context: %s", ret.Context)
	dlog.Infof(c, "Server: %s", ret.Server)

	return ret, nil
}

func (kc *Cluster) WaitUntilReady(ctx context.Context) error {
	select {
	case <-kc.accWait:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (kc *Cluster) GetClusterId(ctx context.Context) string {
	clusterID, _ := actions.GetClusterID(ctx, kc.client)
	return clusterID
}

func (kc *Cluster) Client() *kates.Client {
	return kc.client
}

func (kc *Cluster) GetManagerNamespace() string {
	return kc.kubeconfigExtension.Manager.Namespace
}
