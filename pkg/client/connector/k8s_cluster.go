package connector

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/discovery"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
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

	// The traffic-managers grpc port.
	grpcPort int32

	accLock         sync.Mutex
	accWait         chan struct{}
	watchers        map[string]*k8sWatcher
	localIntercepts map[string]string

	// watcherChanged is a channel that accumulates the channels of all watchers.
	watcherChanged chan struct{}

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

// portForwardAndThen starts a kubectl port-forward command, passes its output to the given scanner, and waits for
// the scanner to produce a result. The then function is started in a new goroutine if the scanner returns something
// other than nil using returned value as an argument. The kubectl port-forward is cancelled when the then function
// returns.
func (kc *k8sCluster) portForwardAndThen(
	c context.Context,
	kpfArgs []string,
	outputHandler func(*bufio.Scanner) interface{},
	then func(context.Context, interface{}) error,
) error {
	args := make([]string, 0, len(kc.flagArgs)+1+len(kpfArgs))
	args = append(args, kc.flagArgs...)
	args = append(args, "port-forward")
	args = append(args, kpfArgs...)

	c, cancel := context.WithCancel(c)
	defer cancel()

	pf := dexec.CommandContext(c, "kubectl", args...)
	out, err := pf.StdoutPipe()
	if err != nil {
		return err
	}
	defer out.Close()

	errBuf := bytes.Buffer{}
	pf.Stderr = &errBuf

	// We want this command to keep on running. If it returns an error, then it was unsuccessful.
	if err = pf.Start(); err != nil {
		out.Close()
		dlog.Errorf(c, "port-forward failed to start: %v", client.RunError(err))
		return err
	}

	sc := bufio.NewScanner(out)

	// Give port-forward at least the port-forward timeout to produce the correct output and spawn the next process
	timer := time.AfterFunc(client.GetConfig(c).Timeouts.TrafficManagerConnect, func() {
		cancel()
	})

	// wait group is done when next process starts.
	var thenErr error
	go func() {
		if output := outputHandler(sc); output != nil {
			timer.Stop() // prevent premature context cancellation
			go func() {
				// discard other output sent to the scanner
				for sc.Scan() {
				}
			}()
			thenErr = then(c, output)
			cancel()
		}
		if errBuf.Len() > 0 {
			dlog.Error(c, errBuf.String())
		}
	}()

	// let the port forward continue running. It will either be killed by the
	// timer (if it didn't produce the expected output) or by a context cancel.
	if err = pf.Wait(); err != nil {
		switch c.Err() {
		case context.Canceled:
			err = thenErr
		case context.DeadlineExceeded:
			err = errors.New("port-forward timed out")
		default:
			err = client.RunError(err)
			dlog.Errorf(c, "port-forward failed: %v", err)
		}
	}
	return err
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
	return client.CheckTimeout(c, &client.GetConfig(c).Timeouts.ClusterConnect, nil)
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

// findSecret returns a secret with the given name in the given namespace or nil
// if no such secret could be found.
func (kc *k8sCluster) findSecret(c context.Context, namespace, name string) (*kates.Secret, error) {
	sec := &kates.Secret{
		TypeMeta:   kates.TypeMeta{Kind: "Secret"},
		ObjectMeta: kates.ObjectMeta{Name: name, Namespace: namespace},
	}
	if err := kc.client.Get(c, sec, sec); err != nil {
		return nil, err
	}
	return sec, nil
}

// findNamespace returns a namespace with the given name or nil
// if no such namespace could be found.
func (kc *k8sCluster) findNamespace(c context.Context, name string) (*kates.Namespace, error) {
	ns := &kates.Namespace{
		TypeMeta:   kates.TypeMeta{Kind: "Namespace"},
		ObjectMeta: kates.ObjectMeta{Name: name},
	}
	if err := kc.client.Get(c, ns, ns); err != nil {
		return nil, err
	}
	return ns, nil
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
func (kc *k8sCluster) findSvc(namespace, name string) *kates.Service {
	var svcCopy *kates.Service
	kc.accLock.Lock()
	if watcher, ok := kc.watchers[namespace]; ok {
		for _, svc := range watcher.Services {
			if svc.Namespace == namespace && svc.Name == name {
				svcCopy = svc.DeepCopy()
				break
			}
		}
	}
	kc.accLock.Unlock()
	return svcCopy
}

// findAllSvc finds services with the given service type in all namespaces of the cluster returns
// a slice containing a copy of those services.
func (kc *k8sCluster) findAllSvcByType(svcType v1.ServiceType) []*kates.Service {
	var svcCopies []*kates.Service
	kc.accLock.Lock()
	for _, watcher := range kc.watchers {
		for _, svc := range watcher.Services {
			if svc.Spec.Type == svcType {
				svcCopies = append(svcCopies, svc.DeepCopy())
				break
			}
		}
	}
	kc.accLock.Unlock()
	return svcCopies
}

// This returns a map of kubernetes object types and the
// number of them that are being watched
func (kc *k8sCluster) findNumK8sObjects() map[string]int {
	objectMap := make(map[string]int)
	var numServices, numEndpoints, numPods int

	kc.accLock.Lock()
	objectMap["namespaces"] = len(kc.watchers)
	for _, watcher := range kc.watchers {
		numServices += len(watcher.Services)
		numEndpoints += len(watcher.Endpoints)
		numPods += len(watcher.Pods)
	}
	kc.accLock.Unlock()

	objectMap["services"] = numServices
	objectMap["endpoints"] = numEndpoints
	objectMap["pods"] = numPods
	return objectMap
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
		return nil, client.CheckTimeout(c, &client.GetConfig(c).Timeouts.ClusterConnect, fmt.Errorf("k8s client create failed: %v", err))
	}

	ret := &k8sCluster{
		k8sConfig:        kubeFlags,
		mappedNamespaces: mappedNamespaces,
		client:           kc,
		daemon:           daemon,
		localIntercepts:  map[string]string{},
		watcherChanged:   make(chan struct{}),
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
		return client.CheckTimeout(ctx, &client.GetConfig(ctx).Timeouts.ClusterConnect, nil)
	}
}

func (kc *k8sCluster) getClusterId(c context.Context) (clusterID string) {
	rootID := func() (rootID string) {
		defer func() {
			// If kates panics, we'll use the default rootID, so we
			// can recover here
			_ = recover()
		}()
		rootID = "00000000-0000-0000-0000-000000000000"

		nsName := "default"
		ns := &kates.Namespace{
			TypeMeta:   kates.TypeMeta{Kind: "Namespace"},
			ObjectMeta: kates.ObjectMeta{Name: nsName},
		}
		if err := kc.client.Get(c, ns, ns); err != nil {
			return
		}

		rootID = string(ns.GetUID())
		return
	}()
	return rootID
}
