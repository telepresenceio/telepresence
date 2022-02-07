package k8s

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/blang/semver"
	"k8s.io/client-go/kubernetes"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

const supportedKubeAPIVersion = "1.17.0"

type NamespaceListener func(context.Context)

// Cluster is a Kubernetes cluster reference
type Cluster struct {
	*Config
	mappedNamespaces []string

	// Main
	ki kubernetes.Interface

	// Current Namespace snapshot, get set by namespace Watcher.
	// The boolean value indicates if this client is allowed to
	// watch services and retrieve workloads in the namespace
	nsWatcher *k8sapi.Watcher

	// nsLock protects currentMappedNamespaces and namespaceListeners
	nsLock sync.Mutex

	// Current Namespace snapshot, filtered by mappedNamespaces
	currentMappedNamespaces map[string]bool

	// Namespace listener. Notified when the currentNamespaces changes
	namespaceListeners []NamespaceListener
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

// check uses a non-caching DiscoveryClientConfig to retrieve the server version
func (kc *Cluster) check(c context.Context) error {
	// The discover client is using context.TODO() so the timeout specified in our
	// context has no effect.
	errCh := make(chan error)
	go func() {
		defer close(errCh)
		info, err := k8sapi.GetK8sInterface(c).Discovery().ServerVersion()
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
// to this client
func (kc *Cluster) namespaceAccessible(namespace string) (exists bool) {
	kc.nsLock.Lock()
	ok := kc.currentMappedNamespaces[namespace]
	kc.nsLock.Unlock()
	return ok
}

func NewCluster(c context.Context, kubeFlags *Config, namespaces []string) (*Cluster, error) {
	rs, err := kubeFlags.ConfigFlags.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(rs)
	if err != nil {
		return nil, err
	}
	c = k8sapi.WithK8sInterface(c, cs)

	if len(namespaces) == 1 && namespaces[0] == "all" {
		namespaces = nil
	} else {
		sort.Strings(namespaces)
	}

	ret := &Cluster{
		Config:           kubeFlags,
		mappedNamespaces: namespaces,
		ki:               cs,
	}

	timedC, cancel := client.GetConfig(c).Timeouts.TimeoutContext(c, client.TimeoutClusterConnect)
	defer cancel()
	if err := ret.check(timedC); err != nil {
		return nil, err
	}

	dlog.Infof(c, "Context: %s", ret.Context)
	dlog.Infof(c, "Server: %s", ret.Server)

	ret.startNamespaceWatcher(c)
	return ret, nil
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

func (kc *Cluster) WithK8sInterface(c context.Context) context.Context {
	return k8sapi.WithK8sInterface(c, kc.ki)
}
