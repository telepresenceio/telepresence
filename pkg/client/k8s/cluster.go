package k8s

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver/v4"
	"github.com/cenkalti/backoff/v4"
	auth "k8s.io/api/authorization/v1"
	core "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

const (
	supportedKubeAPIVersion = "1.17.0"
	defaultManagerNamespace = "ambassador"
)

type NamespaceListener func()

// Cluster is a Kubernetes cluster reference.
type Cluster struct {
	*Kubeconfig
	MappedNamespaces []string

	// nsLock protects namespaceWatcherSnapshot, currentMappedNamespaces and namespaceEventHandlers
	nsLock sync.Mutex

	// snapshot maintained by the namespaces watcher.
	namespaceWatcherSnapshot map[string]struct{}

	// Current Namespace snapshot, filtered by MappedNamespaces
	currentMappedNamespaces map[string]bool

	// Namespace listener. Notified when the currentNamespaces changes
	namespaceEventHandlers []NamespaceListener
}

func (kc *Cluster) ActualNamespace(namespace string) string {
	if namespace == "" {
		namespace = kc.Namespace
	}
	if !kc.namespaceAccessible(namespace) {
		namespace = ""
	}
	return namespace
}

// check uses a non-caching DiscoveryClientConfig to retrieve the server version.
func (kc *Cluster) check(c context.Context) error {
	// The discover client is using context.TODO() so the timeout specified in our
	// context has no effect.
	var info *version.Info
	dsc := k8sapi.GetK8sInterface(kc).Discovery()
	err := backoff.Retry(func() (err error) {
		if info, err = dsc.ServerVersion(); err != nil {
			if !strings.Contains(err.Error(), "connection refused") {
				err = backoff.Permanent(err)
			}
		}
		return err
	}, backoff.WithContext(backoff.WithMaxRetries(backoff.NewConstantBackOff(400*time.Millisecond), 4), c))
	if err != nil {
		return fmt.Errorf("initial cluster check failed: %w", client.RunError(err))
	}
	// Validate that the kubernetes server version is supported
	dlog.Infof(c, "Server version %s", info.GitVersion)
	gitVer, err := semver.Parse(strings.TrimPrefix(info.GitVersion, "v"))
	if err != nil {
		return fmt.Errorf("error converting version %s to semver: %s", info.GitVersion, err)
	}
	supGitVer, err := semver.Parse(supportedKubeAPIVersion)
	if err != nil {
		return fmt.Errorf("error converting known version %s to semver: %s", supportedKubeAPIVersion, err)
	}
	if gitVer.LT(supGitVer) {
		return fmt.Errorf("kubernetes server versions older than %s are not supported, using %s", supportedKubeAPIVersion, info.GitVersion)
	}
	return nil
}

func (kc *Cluster) CheckTrafficManagerService(ctx context.Context, namespace string) error {
	dlog.Debug(ctx, "checking that traffic-manager exists")
	coreV1 := k8sapi.GetK8sInterface(kc).CoreV1()
	if _, err := coreV1.Services(namespace).Get(ctx, agentconfig.ManagerAppName, meta.GetOptions{}); err != nil {
		msg := fmt.Sprintf("unable to get service %s in %s: %v", agentconfig.ManagerAppName, namespace, err)
		se := &k8serrors.StatusError{}
		if errors.As(err, &se) {
			if se.Status().Code == http.StatusNotFound {
				dlog.Error(ctx, msg)
				msg = "traffic manager not found, if it is not installed, please run 'telepresence helm install'. " +
					"If it is installed, try connecting with a --manager-namespace to point telepresence to the namespace it's installed in."
			}
		}
		return errcat.User.New(msg)
	}
	return nil
}

// namespaceAccessible answers the question if the namespace is present and accessible
// to this client.
func (kc *Cluster) namespaceAccessible(namespace string) (exists bool) {
	kc.nsLock.Lock()
	ok := kc.currentMappedNamespaces[namespace]
	kc.nsLock.Unlock()
	return ok
}

func NewCluster(kubeFlags *Kubeconfig, namespaces []string) (*Cluster, error) {
	ret := &Cluster{Kubeconfig: kubeFlags}

	cfg := client.GetConfig(ret)
	timedC, cancel := cfg.Timeouts().TimeoutContext(kubeFlags, client.TimeoutClusterConnect)
	defer cancel()
	if err := ret.check(timedC); err != nil {
		return nil, err
	}

	dlog.Infof(ret, "Context: %s", ret.KubeContext)
	dlog.Infof(ret, "Server: %s", ret.Server)

	if len(namespaces) == 1 && namespaces[0] == "all" {
		namespaces = nil
	}
	if len(namespaces) == 0 {
		namespaces = cfg.Cluster().MappedNamespaces
	}
	if len(namespaces) == 0 {
		if k8sapi.CanWatchNamespaces(ret) {
			dlog.Infof(ret, "Will watch all namespaces")
			ret.StartNamespaceWatcher()
		} else {
			dlog.Warnf(ret, "Unable to watch all namespaces")
		}
	} else {
		dlog.Infof(ret, "Will use mapped namespaces %s", namespaces)
		ret.SetMappedNamespaces(namespaces)
	}
	if GetManagerNamespace(ret) == "" {
		tns, err := ret.determineTrafficManagerNamespace()
		if err != nil {
			return nil, err
		}
		cfg.Cluster().DefaultManagerNamespace = tns
	}
	dlog.Infof(ret, "Will look for traffic manager in namespace %s", GetManagerNamespace(ret))
	return ret, nil
}

func parseCIDR(cidr []string) ([]netip.Prefix, error) {
	if len(cidr) == 0 {
		return nil, nil
	}
	result := make([]netip.Prefix, len(cidr))
	for i := range cidr {
		ipNet, err := netip.ParsePrefix(cidr[i])
		if err != nil {
			return nil, fmt.Errorf("failed to parse CIDR %s: %w", cidr[i], err)
		}
		result[i] = ipNet
	}
	return result, nil
}

func ConnectCluster(cr *rpc.ConnectRequest, config *Kubeconfig) (*Cluster, error) {
	mappedNamespaces := cr.MappedNamespaces
	if len(mappedNamespaces) == 1 && mappedNamespaces[0] == "all" {
		mappedNamespaces = nil
	} else {
		sort.Strings(mappedNamespaces)
	}

	cluster, err := NewCluster(config, mappedNamespaces)
	if err != nil {
		return nil, err
	}

	extraAlsoProxy, err := parseCIDR(cr.GetAlsoProxy())
	if err != nil {
		return nil, fmt.Errorf("failed to parse extra also proxy: %w", err)
	}

	extraNeverProxy, err := parseCIDR(cr.GetNeverProxy())
	if err != nil {
		return nil, fmt.Errorf("failed to parse extra never proxy: %w", err)
	}

	extraAllow, err := parseCIDR(cr.GetAllowConflictingSubnets())
	if err != nil {
		return nil, fmt.Errorf("failed to parse extra allow conflicting subnets: %w", err)
	}
	if len(extraAlsoProxy)+len(extraNeverProxy)+len(extraAllow) > 0 {
		cfg := client.GetConfig(cluster)
		rt := cfg.Routing()
		rt.AllowConflicting = append(rt.AllowConflicting, extraAllow...)
		rt.AlsoProxy = append(rt.AlsoProxy, extraAlsoProxy...)
		rt.NeverProxy = append(rt.NeverProxy, extraNeverProxy...)
		client.ReplaceConfig(cluster, cfg)
	}

	return cluster, nil
}

// determineTrafficManagerNamespace finds the namespace for the traffic-manager. It is determined by the following steps:
//
//  1. If a traffic-manager service is found in one of the currently accessible namespaces, return it.
//  2. If the client has access to the default manager namespace, then return it.
//  3. If the client has access to the default namespace, then return it.
//  4. Return an error stating that it isn't possible to determine the namespace.
func (kc *Cluster) determineTrafficManagerNamespace() (string, error) {
	// Search for the traffic-manager in mapped namespaces
	nss := kc.GetCurrentNamespaces(true)
	for _, ns := range nss {
		if _, err := k8sapi.GetService(kc, agentconfig.ManagerAppName, ns); err == nil {
			return ns, nil
		}
	}

	// No existing manager was found.
	if canGetDefaultTrafficManagerService(kc) {
		return defaultManagerNamespace, nil
	}

	// No existing traffic-manager found. Assume that it should be installed
	// in the default namespace if it is accessible
	if canAccessNS(kc, kc.Namespace) {
		return kc.Namespace, nil
	}
	return "", errcat.User.New("unable to determine the traffic-manager namespace")
}

// GetCurrentNamespaces returns the names of the namespaces that this client
// is mapping. If the forClientAccess is true, then the namespaces are restricted
// to those where an intercept can take place, i.e. the namespaces where this
// client can get pods.
func (kc *Cluster) GetCurrentNamespaces(forClientAccess bool) []string {
	kc.nsLock.Lock()
	nss := make([]string, 0, len(kc.currentMappedNamespaces))
	if forClientAccess {
		for ns, ok := range kc.currentMappedNamespaces {
			if ok {
				nss = append(nss, ns)
			}
		}
	} else {
		for ns := range kc.currentMappedNamespaces {
			nss = append(nss, ns)
		}
	}
	kc.nsLock.Unlock()
	sort.Strings(nss)
	return nss
}

func (kc *Cluster) GetManagerInstallId() string {
	managerID, _ := k8sapi.GetNamespaceID(kc, GetManagerNamespace(kc))
	return managerID
}

func GetManagerNamespace(ctx context.Context) string {
	return client.GetConfig(ctx).Cluster().DefaultManagerNamespace
}

// canGetDefaultTrafficManagerService answers the question if this client has the RBAC permissions
// necessary to get the traffic-manager in the default namespace.
func canGetDefaultTrafficManagerService(ctx context.Context) bool {
	ok, err := k8sapi.CanI(ctx, &auth.ResourceAttributes{
		Verb:      "get",
		Resource:  "services",
		Name:      agentconfig.ManagerAppName,
		Namespace: defaultManagerNamespace,
	})
	return err == nil && ok
}

// canAccessNS answers the question if this client has the RBAC permissions
// necessary to get a pod in the given namespace.
func canAccessNS(ctx context.Context, namespace string) bool {
	ok, err := k8sapi.CanI(ctx, &auth.ResourceAttributes{
		Namespace: namespace,
		Verb:      "get",
		Resource:  "pods",
		Group:     "",
	})
	return err == nil && ok
}

// StartNamespaceWatcher runs a Kubernetes Watcher that provide information about the cluster's namespaces'.
// The function waits for the first snapshot to arrive before returning.
func (kc *Cluster) StartNamespaceWatcher() {
	kc.namespaceWatcherSnapshot = make(map[string]struct{})
	nsSynced := make(chan struct{})
	go func() {
		api := k8sapi.GetK8sInterface(kc).CoreV1()
		for kc.Err() == nil {
			w, err := api.Namespaces().Watch(kc, meta.ListOptions{})
			if err != nil {
				dlog.Errorf(kc, "unable to create service watcher: %v", err)
				return
			}
			kc.namespacesEventHandler(w.ResultChan(), nsSynced)
		}
	}()
	select {
	case <-kc.Done():
	case <-nsSynced:
	}
}

func (kc *Cluster) namespacesEventHandler(evCh <-chan watch.Event, nsSynced chan struct{}) {
	// The delay timer will initially sleep forever. It's reset to a very short
	// delay when the file is modified.
	delay := time.AfterFunc(time.Duration(math.MaxInt64), func() {
		kc.refreshNamespaces()
		select {
		case <-nsSynced:
		default:
			close(nsSynced)
		}
	})
	defer delay.Stop()

	for {
		select {
		case <-kc.Done():
			return
		case event, ok := <-evCh:
			if !ok {
				return // restart watcher
			}
			ns, ok := event.Object.(*core.Namespace)
			if !ok {
				continue
			}
			kc.nsLock.Lock()
			switch event.Type {
			case watch.Deleted:
				delete(kc.namespaceWatcherSnapshot, ns.Name)
			case watch.Added, watch.Modified:
				kc.namespaceWatcherSnapshot[ns.Name] = struct{}{}
			}
			kc.nsLock.Unlock()

			// We consider the watcher synced after 10 ms of inactivity. It's not a big deal
			// if more namespaces arrive after that.
			delay.Reset(10 * time.Millisecond)
		}
	}
}

func (kc *Cluster) SetMappedNamespaces(namespaces []string) bool {
	sort.Strings(namespaces)
	if !slices.Equal(namespaces, kc.MappedNamespaces) {
		kc.MappedNamespaces = namespaces
		kc.refreshNamespaces()
		return true
	}
	return false
}

func (kc *Cluster) AddNamespaceEventHandler(nsEventHandler NamespaceListener) {
	kc.nsLock.Lock()
	kc.namespaceEventHandlers = append(kc.namespaceEventHandlers, nsEventHandler)
	kc.nsLock.Unlock()
	nsEventHandler()
}

func (kc *Cluster) refreshNamespaces() {
	kc.nsLock.Lock()
	var nss []string
	if kc.namespaceWatcherSnapshot == nil {
		// No permission to watch namespaces. Use the mapped-namespaces instead.
		nss = kc.MappedNamespaces
		if len(nss) == 0 {
			// No mapped namespaces exists. Fallback to what's defined in the kube-context (will be "default" if none was defined).
			nss = []string{kc.Namespace}
		}
	} else {
		nss = make([]string, len(kc.namespaceWatcherSnapshot))
		i := 0
		for ns := range kc.namespaceWatcherSnapshot {
			nss[i] = ns
			i++
		}
	}
	namespaces := make(map[string]bool, len(nss))
	for _, ns := range nss {
		if kc.shouldBeWatched(ns) {
			accessOk, ok := kc.currentMappedNamespaces[ns]
			if !ok {
				accessOk = canAccessNS(kc, ns)
			}
			namespaces[ns] = accessOk
		}
	}
	if maps.Equal(namespaces, kc.currentMappedNamespaces) {
		kc.nsLock.Unlock()
	} else {
		dlog.Debugf(kc, "Namespaces changed: %v", namespaces)
		kc.currentMappedNamespaces = namespaces
		nsListeners := slices.Clone(kc.namespaceEventHandlers)
		kc.nsLock.Unlock()
		for _, nsListener := range nsListeners {
			nsListener()
		}
	}
}

func (kc *Cluster) shouldBeWatched(namespace string) bool {
	if len(kc.MappedNamespaces) == 0 {
		return true
	}
	for _, n := range kc.MappedNamespaces {
		if n == namespace {
			return true
		}
	}
	return false
}
