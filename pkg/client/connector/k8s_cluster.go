package connector

import (
	"context"
	"fmt"
	"sync"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
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

// k8sCluster is a Kubernetes cluster reference
type k8sCluster struct {
	*k8sConfig
	mappedNamespaces []string

	// Main
	client *kates.Client
	daemon daemon.DaemonClient // for DNS updates

	lastNamespaces []string

	// Currently intercepted namespaces by remote intercepts
	interceptedNamespaces map[string]struct{}

	// Currently intercepted namespaces by local intercepts
	localInterceptedNamespaces map[string]struct{}

	accLock         sync.Mutex
	accWait         chan struct{}
	localIntercepts map[string]string

	// Current Namespace snapshot, get set by acc.Update().
	Namespaces []*objName
}

func (kc *k8sCluster) actualNamespace(namespace string) string {
	if namespace == "" {
		namespace = kc.Namespace
	}
	if !kc.namespaceExists(namespace) {
		namespace = ""
	}
	return namespace
}

// check uses a non-caching DiscoveryClientConfig to retrieve the server version
func (kc *k8sCluster) check(c context.Context) error {
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
func (kc *k8sCluster) kindNames(c context.Context, kind, namespace string) ([]string, error) {
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

// deploymentNames returns the names of all deployments found in the given Namespace
func (kc *k8sCluster) deploymentNames(c context.Context, namespace string) ([]string, error) {
	return kc.kindNames(c, "Deployment", namespace)
}

// replicaSetNames returns the names of all replica sets found in the given Namespace
func (kc *k8sCluster) replicaSetNames(c context.Context, namespace string) ([]string, error) {
	return kc.kindNames(c, "ReplicaSet", namespace)
}

// statefulSetNames returns the names of all replica sets found in the given Namespace
func (kc *k8sCluster) statefulSetNames(c context.Context, namespace string) ([]string, error) {
	return kc.kindNames(c, "StatefulSet", namespace)
}

// PodSetNames returns the names of all replica sets found in the given Namespace
func (kc *k8sCluster) podNames(c context.Context, namespace string) ([]string, error) {
	return kc.kindNames(c, "Pod", namespace)
}

// findDeployment returns a deployment with the given name in the given namespace or nil
// if no such deployment could be found.
func (kc *k8sCluster) findDeployment(c context.Context, namespace, name string) (*kates.Deployment, error) {
	dep := &kates.Deployment{
		TypeMeta:   kates.TypeMeta{Kind: "Deployment"},
		ObjectMeta: kates.ObjectMeta{Name: name, Namespace: namespace},
	}
	if err := kc.client.Get(c, dep, dep); err != nil {
		return nil, err
	}
	return dep, nil
}

// findStatefulSet returns a statefulSet with the given name in the given namespace or nil
// if no such statefulSet could be found.
func (kc *k8sCluster) findStatefulSet(c context.Context, namespace, name string) (*kates.StatefulSet, error) {
	statefulSet := &kates.StatefulSet{
		TypeMeta:   kates.TypeMeta{Kind: "StatefulSet"},
		ObjectMeta: kates.ObjectMeta{Name: name, Namespace: namespace},
	}
	if err := kc.client.Get(c, statefulSet, statefulSet); err != nil {
		return nil, err
	}
	return statefulSet, nil
}

// findReplicaSet returns a replica set with the given name in the given namespace or nil
// if no such replica set could be found.
func (kc *k8sCluster) findReplicaSet(c context.Context, namespace, name string) (*kates.ReplicaSet, error) {
	rs := &kates.ReplicaSet{
		TypeMeta:   kates.TypeMeta{Kind: "ReplicaSet"},
		ObjectMeta: kates.ObjectMeta{Name: name, Namespace: namespace},
	}
	if err := kc.client.Get(c, rs, rs); err != nil {
		return nil, err
	}
	return rs, nil
}

// findPod returns a pod with the given name in the given namespace or nil
// if no such replica set could be found.
func (kc *k8sCluster) findPod(c context.Context, namespace, name string) (*kates.Pod, error) {
	pod := &kates.Pod{
		TypeMeta:   kates.TypeMeta{Kind: "Pod"},
		ObjectMeta: kates.ObjectMeta{Name: name, Namespace: namespace},
	}
	if err := kc.client.Get(c, pod, pod); err != nil {
		return nil, err
	}
	return pod, nil
}

// findObjectKind returns a workload for the given name and namespace. We
// search in a specific order based on how we prefer workload objects:
// 1. Deployments
// 2. ReplicaSets
// 3. StatefulSets
// And return the kind as soon as we find one that matches
func (kc *k8sCluster) findObjectKind(c context.Context, namespace, name string) (string, error) {
	depNames, err := kc.deploymentNames(c, namespace)
	if err != nil {
		return "", err
	}
	for _, depName := range depNames {
		if depName == name {
			return "Deployment", nil
		}
	}

	// Since Deployments manage ReplicaSets, we only look for matching
	// ReplicaSets if no Deployment was found
	rsNames, err := kc.replicaSetNames(c, namespace)
	if err != nil {
		return "", err
	}
	for _, rsName := range rsNames {
		if rsName == name {
			return "ReplicaSet", nil
		}
	}

	// Like ReplicaSets, StatefulSets only manage pods so we check for
	// them next
	statefulSetNames, err := kc.statefulSetNames(c, namespace)
	if err != nil {
		return "", err
	}
	for _, statefulSetName := range statefulSetNames {
		if statefulSetName == name {
			return "StatefulSet", nil
		}
	}
	return "", errors.New("No supported Object Kind Found")
}

// findSvc finds a service with the given name in the given Namespace and returns
// either a copy of that service or nil if no such service could be found.
func (kc *k8sCluster) findSvc(c context.Context, namespace, name string) (*kates.Service, error) {
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
func (kc *k8sCluster) findAllSvcByType(c context.Context, svcType v1.ServiceType) ([]*kates.Service, error) {
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

func (kc *k8sCluster) namespaceExists(namespace string) (exists bool) {
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

func newKCluster(c context.Context, kubeFlags *k8sConfig, mappedNamespaces []string, daemon daemon.DaemonClient) (*k8sCluster, error) {
	// TODO: Add constructor to kates that takes an additional restConfig argument to prevent that kates recreates it.
	kc, err := kates.NewClientFromConfigFlags(kubeFlags.configFlags)
	if err != nil {
		return nil, client.CheckTimeout(c, fmt.Errorf("k8s client create failed: %w", err))
	}

	ret := &k8sCluster{
		k8sConfig:        kubeFlags,
		mappedNamespaces: mappedNamespaces,
		client:           kc,
		daemon:           daemon,
		localIntercepts:  map[string]string{},
		accWait:          make(chan struct{}),
	}

	if err := ret.check(c); err != nil {
		return nil, err
	}

	dlog.Infof(c, "Context: %s", ret.Context)
	dlog.Infof(c, "Server: %s", ret.Server)

	return ret, nil
}

func (kc *k8sCluster) waitUntilReady(ctx context.Context) error {
	select {
	case <-kc.accWait:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (kc *k8sCluster) getClusterId(ctx context.Context) string {
	clusterID, _ := actions.GetClusterID(ctx, kc.client)
	return clusterID
}
