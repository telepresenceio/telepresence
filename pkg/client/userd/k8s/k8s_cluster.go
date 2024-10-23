package k8s

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver/v4"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes"

	argorollouts "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8sclient"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

const (
	supportedKubeAPIVersion = "1.17.0"
	defaultManagerNamespace = "ambassador"
)

// Cluster is a Kubernetes cluster reference.
type Cluster struct {
	*client.Kubeconfig
	MappedNamespaces []string

	// Main
	ki kubernetes.Interface

	// Argo Rollouts
	ari argorollouts.Interface

	// nsLock protects namespaceWatcherSnapshot, currentMappedNamespaces and namespaceListeners
	nsLock sync.Mutex

	// snapshot maintained by the namespaces watcher.
	namespaceWatcherSnapshot map[string]struct{}

	// Current Namespace snapshot, filtered by MappedNamespaces
	currentMappedNamespaces map[string]bool

	// Namespace listener. Notified when the currentNamespaces changes
	namespaceListeners []userd.NamespaceListener
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
	errCh := make(chan error)
	go func() {
		defer close(errCh)
		var info *version.Info
		var err error
		for attempts := 0; attempts < 4; attempts++ {
			if info, err = k8sapi.GetK8sInterface(c).Discovery().ServerVersion(); err != nil {
				if strings.Contains(err.Error(), "connection refused") {
					dlog.Warnf(c, "Connection to connect failed, retry %d", attempts+1)
					dtime.SleepWithContext(c, 400*time.Millisecond)
					continue
				}
			}
			break
		}
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

// namespaceAccessible answers the question if the namespace is present and accessible
// to this client.
func (kc *Cluster) namespaceAccessible(namespace string) (exists bool) {
	kc.nsLock.Lock()
	ok := kc.currentMappedNamespaces[namespace]
	kc.nsLock.Unlock()
	return ok
}

func NewCluster(c context.Context, kubeFlags *client.Kubeconfig, namespaces []string) (context.Context, *Cluster, error) {
	rs := kubeFlags.RestConfig
	cs, err := kubernetes.NewForConfig(rs)
	if err != nil {
		return c, nil, err
	}
	acs, err := argorollouts.NewForConfig(rs)
	if err != nil {
		return c, nil, err
	}
	c = k8sapi.WithJoinedClientSetInterface(c, cs, acs)

	ret := &Cluster{
		Kubeconfig: kubeFlags,
		ki:         cs,
		ari:        acs,
	}

	cfg := client.GetConfig(c)
	timedC, cancel := cfg.Timeouts().TimeoutContext(c, client.TimeoutClusterConnect)
	defer cancel()
	if err = ret.check(timedC); err != nil {
		return c, nil, err
	}

	dlog.Infof(c, "Context: %s", ret.Context)
	dlog.Infof(c, "Server: %s", ret.Server)

	if len(namespaces) == 1 && namespaces[0] == "all" {
		namespaces = nil
	}
	if len(namespaces) == 0 {
		namespaces = cfg.Cluster().MappedNamespaces
	}
	if len(namespaces) == 0 {
		if k8sclient.CanWatchNamespaces(c) {
			ret.StartNamespaceWatcher(c)
		}
	} else {
		ret.SetMappedNamespaces(c, namespaces)
	}
	if GetManagerNamespace(c) == "" {
		tns, err := ret.determineTrafficManagerNamespace(c)
		if err != nil {
			return c, nil, err
		}
		nc := client.GetDefaultConfig()
		nc.Cluster().DefaultManagerNamespace = tns
		c = client.WithConfig(c, cfg.Merge(nc))
	}
	dlog.Infof(c, "Will look for traffic manager in namespace %s", GetManagerNamespace(c))
	return c, ret, nil
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

func ConnectCluster(c context.Context, cr *rpc.ConnectRequest, config *client.Kubeconfig) (context.Context, *Cluster, error) {
	mappedNamespaces := cr.MappedNamespaces
	if len(mappedNamespaces) == 1 && mappedNamespaces[0] == "all" {
		mappedNamespaces = nil
	} else {
		sort.Strings(mappedNamespaces)
	}

	c, cluster, err := NewCluster(c, config, mappedNamespaces)
	if err != nil {
		return c, nil, err
	}

	extraAlsoProxy, err := parseCIDR(cr.GetAlsoProxy())
	if err != nil {
		return c, nil, fmt.Errorf("failed to parse extra also proxy: %w", err)
	}

	extraNeverProxy, err := parseCIDR(cr.GetNeverProxy())
	if err != nil {
		return c, nil, fmt.Errorf("failed to parse extra never proxy: %w", err)
	}

	extraAllow, err := parseCIDR(cr.GetAllowConflictingSubnets())
	if err != nil {
		return c, nil, fmt.Errorf("failed to parse extra allow conflicting subnets: %w", err)
	}
	if len(extraAlsoProxy)+len(extraNeverProxy)+len(extraAllow) > 0 {
		cfg := client.GetDefaultConfig()
		rt := cfg.Routing()
		rt.AllowConflicting = append(rt.AllowConflicting, extraAllow...)
		rt.AlsoProxy = append(rt.AlsoProxy, extraAlsoProxy...)
		rt.NeverProxy = append(rt.NeverProxy, extraNeverProxy...)
		c = client.WithConfig(c, client.GetConfig(c).Merge(cfg))
	}

	return c, cluster, nil
}

// determineTrafficManagerNamespace finds the namespace for the traffic-manager. It is determined by the following steps:
//
//  1. If a treffic-manager service is found in one of the currently accessible namespaces, return it.
//  2. If the client has access to the default manager namespace, then return it.
//  3. If the client has access to the default namespace, then return it.
//  4. Return an error stating that it isn't possible to determine the namespace.
func (kc *Cluster) determineTrafficManagerNamespace(c context.Context) (string, error) {
	// Search for the traffic-manager in mapped namespaces
	nss := kc.GetCurrentNamespaces(true)
	for _, ns := range nss {
		if _, err := k8sapi.GetService(c, "traffic-manager", ns); err == nil {
			return ns, nil
		}
	}

	// No existing manager was found.
	if canGetDefaultTrafficManagerService(c) {
		return defaultManagerNamespace, nil
	}

	// No existing traffic-manager found. Assume that it should be installed
	// in the default namespace if it is accessible
	if canAccessNS(c, kc.Namespace) {
		return kc.Namespace, nil
	}
	return "", errcat.User.New("unable to determine the traffic-manager namespace")
}

// GetCurrentNamespaces returns the names of the namespaces that this client
// is mapping. If the forClientAccess is true, then the namespaces are restricted
// to those where an intercept can take place, i.e. the namespaces where this
// client can Watch and get services and deployments.
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

func (kc *Cluster) GetClusterId(ctx context.Context) string {
	clusterID, _ := k8sapi.GetClusterID(ctx)
	return clusterID
}

func (kc *Cluster) GetManagerInstallId(ctx context.Context) string {
	managerID, _ := k8sapi.GetNamespaceID(ctx, GetManagerNamespace(ctx))
	return managerID
}

func GetManagerNamespace(ctx context.Context) string {
	return client.GetConfig(ctx).Cluster().DefaultManagerNamespace
}

func (kc *Cluster) WithJoinedClientSetInterface(c context.Context) context.Context {
	return k8sapi.WithJoinedClientSetInterface(c, kc.ki, kc.ari)
}
