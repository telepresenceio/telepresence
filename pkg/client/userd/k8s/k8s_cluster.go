package k8s

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/blang/semver"
	corev1 "k8s.io/api/core/v1"
	k8err "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"

	"github.com/datawire/ambassador/v2/pkg/kates"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/actions"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

const supportedKubeAPIVersion = "1.17.0"

type nameMeta struct {
	Name string `json:"name"`
}

type objName struct {
	nameMeta `json:"metadata"`
}

type ResourceFinder interface {
	FindDeployment(c context.Context, namespace, name string) (*kates.Deployment, error)
	FindPod(c context.Context, namespace, name string) (*kates.Pod, error)
	FindSvc(c context.Context, namespace, name string) (*kates.Service, error)
}

// Cluster is a Kubernetes cluster reference
type Cluster struct {
	*Config
	mappedNamespaces []string

	ingressInfo []*manager.IngressInfo

	// Main
	client *kates.Client
	ki     kubernetes.Interface

	// search paths are propagated to the rootDaemon
	rootDaemon daemon.DaemonClient

	lastNamespaces []string

	// Currently intercepted namespaces by remote intercepts
	interceptedNamespaces map[string]struct{}

	// Currently intercepted namespaces by local intercepts
	localInterceptedNamespaces map[string]struct{}

	accLock         sync.Mutex
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
		// Validate that the kubernetes server version is supported
		dlog.Infof(c, "Server version %s", info.GitVersion)
		gitVer, err := semver.Parse(strings.TrimPrefix(info.GitVersion, "v"))
		if err != nil {
			dlog.Errorf(c, "error converting version %s to semver: %s", info.GitVersion, err)
		}
		supGitVer, err := semver.Parse(supportedKubeAPIVersion)
		if err != nil {
			dlog.Errorf(c, "error converting known version %s to semver: %s", supportedKubeAPIVersion, err)
		}
		if gitVer.LT(supGitVer) {
			dlog.Errorf(c,
				"kubernetes server versions older than %s are not supported, using %s .",
				supportedKubeAPIVersion, info.GitVersion)
		}
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

// Deployments returns all deployments found in the given Namespace
func (kc *Cluster) Deployments(c context.Context, namespace string) ([]kates.Object, error) {
	var deployments []*kates.Deployment
	if err := kc.client.List(c, kates.Query{Kind: "Deployment", Namespace: namespace}, &deployments); err != nil {
		return nil, err
	}
	objs := make([]kates.Object, len(deployments))
	for i, dep := range deployments {
		objs[i] = dep
	}
	return objs, nil
}

// ReplicaSets returns all replica sets found in the given Namespace
func (kc *Cluster) ReplicaSets(c context.Context, namespace string) ([]kates.Object, error) {
	var replicaSets []*kates.ReplicaSet
	if err := kc.client.List(c, kates.Query{Kind: "ReplicaSet", Namespace: namespace}, &replicaSets); err != nil {
		return nil, err
	}
	objs := make([]kates.Object, len(replicaSets))
	for i, rs := range replicaSets {
		objs[i] = rs
	}
	return objs, nil
}

// StatefulSets returns all stateful sets found in the given Namespace
func (kc *Cluster) StatefulSets(c context.Context, namespace string) ([]kates.Object, error) {
	var statefulSets []*kates.StatefulSet
	if err := kc.client.List(c, kates.Query{Kind: "StatefulSet", Namespace: namespace}, &statefulSets); err != nil {
		return nil, err
	}
	objs := make([]kates.Object, len(statefulSets))
	for i, ss := range statefulSets {
		objs[i] = ss
	}
	return objs, nil
}

// Pods returns all pods found in the given Namespace
func (kc *Cluster) Pods(c context.Context, namespace string) ([]*kates.Pod, error) {
	var pods []*kates.Pod
	if err := kc.client.List(c, kates.Query{Kind: "Pod", Namespace: namespace}, &pods); err != nil {
		return nil, err
	}
	return pods, nil
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

// FindAgain returns a fresh version of the given object.
func (kc *Cluster) FindAgain(c context.Context, obj kates.Object) (kates.Object, error) {
	if err := kc.client.Get(c, obj, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// FindPodFromSelector returns a pod with the given name-hex-hex
func (kc *Cluster) FindPodFromSelector(c context.Context, namespace string, selector map[string]string) (*kates.Pod, error) {
	pods, err := kc.Pods(c, namespace)
	if err != nil {
		return nil, err
	}

	for _, pod := range pods {
		podLabels := pod.GetLabels()
		match := true
		// check if selector is in labels
		for key, val := range selector {
			if podLabels[key] != val {
				match = false
				break
			}
		}
		if match {
			return pod, nil
		}
	}

	return nil, errors.New("pod not found")
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

// FindWorkload returns a workload for the given name, namespace, and workloadKind. The workloadKind
// is optional. A search is performed in the following order if it is empty:
//
//   1. Deployments
//   2. ReplicaSets
//   3. StatefulSets
//
// The first match is returned.
func (kc *Cluster) FindWorkload(c context.Context, namespace, name, workloadKind string) (kates.Object, error) {
	type workLoad struct {
		kind string
		obj  kates.Object
	}
	for _, wl := range []workLoad{{"Deployment", &kates.Deployment{}}, {"ReplicaSet", &kates.ReplicaSet{}}, {"StatefulSet", &kates.StatefulSet{}}} {
		if workloadKind != "" && workloadKind != wl.kind {
			continue
		}
		wl.obj.(schema.ObjectKind).SetGroupVersionKind(schema.GroupVersionKind{Kind: wl.kind})
		wl.obj.SetName(name)
		wl.obj.SetNamespace(namespace)
		dlog.Debugf(c, "Get %s %s.%s", wl.kind, name, namespace)
		if err := kc.client.Get(c, wl.obj, wl.obj); err != nil {
			if kates.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		return wl.obj, nil
	}
	if workloadKind == "" {
		workloadKind = "workload"
	}
	return nil, k8err.NewNotFound(corev1.Resource(workloadKind), name+"."+namespace)
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

// findAllSvcByType finds services with the given service type in all namespaces of the cluster returns
// a slice containing a copy of those services.
func (kc *Cluster) findAllSvcByType(c context.Context, svcType corev1.ServiceType) ([]*kates.Service, error) {
	// NOTE: This is expensive in terms of bandwidth on a large cluster. We currently only use this
	// to retrieve ingress info and that task could be moved to the traffic-manager instead.
	var typedSvcs []*kates.Service
	findTyped := func(ns string) error {
		var svcs []*kates.Service
		if err := kc.client.List(c, kates.Query{Kind: "Service", Namespace: ns}, &svcs); err != nil {
			return err
		}
		for _, svc := range svcs {
			if svc.Spec.Type == svcType {
				typedSvcs = append(typedSvcs, svc)
			}
		}
		return nil
	}

	kc.accLock.Lock()
	var mns []string
	if len(kc.mappedNamespaces) > 0 {
		mns = make([]string, len(kc.mappedNamespaces))
		copy(mns, kc.mappedNamespaces)
	}
	kc.accLock.Unlock()

	if len(mns) > 0 {
		for _, ns := range mns {
			if err := findTyped(ns); err != nil {
				return nil, err
			}
		}
	} else if err := findTyped(""); err != nil {
		return nil, err
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

func NewCluster(c context.Context, kubeFlags *Config, namespaces []string, rootDaemon daemon.DaemonClient) (*Cluster, error) {
	// TODO: Add constructor to kates that takes an additional restConfig argument to prevent that kates recreates it.
	kc, err := kates.NewClientFromConfigFlags(kubeFlags.ConfigFlags)
	if err != nil {
		return nil, client.CheckTimeout(c, fmt.Errorf("k8s client create failed: %w", err))
	}
	rs, err := kubeFlags.ConfigFlags.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	ki, err := kubernetes.NewForConfig(rs)
	if err != nil {
		return nil, err
	}
	if len(namespaces) == 1 && namespaces[0] == "all" {
		namespaces = nil
	} else {
		sort.Strings(namespaces)
	}

	ret := &Cluster{
		Config:           kubeFlags,
		mappedNamespaces: namespaces,
		client:           kc,
		ki:               ki,
		rootDaemon:       rootDaemon,
		LocalIntercepts:  map[string]string{},
	}

	if err := ret.check(c); err != nil {
		return nil, err
	}

	dlog.Infof(c, "Context: %s", ret.Context)
	dlog.Infof(c, "Server: %s", ret.Server)

	firstSnapshotArrived := make(chan struct{})
	go func() {
		if err := ret.nsWatcher(dgroup.WithGoroutineName(c, "ns-watcher"), firstSnapshotArrived); err != nil {
			dlog.Error(c, err)
		}
	}()
	select {
	case <-c.Done():
	case <-firstSnapshotArrived:
	}
	return ret, nil
}

func (kc *Cluster) GetClusterId(ctx context.Context) string {
	clusterID, _ := actions.GetClusterID(ctx, kc.client)
	return clusterID
}

func (kc *Cluster) WithK8sInterface(c context.Context) context.Context {
	return k8sapi.WithK8sInterface(c, kc.ki)
}

func (kc *Cluster) Client() *kates.Client {
	return kc.client
}
