package k8s

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	auth "k8s.io/api/authorization/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/slice"
)

// nsWatcher runs a Kubernetes Watcher that provide information about the cluster's namespaces'.
//
// A filtered list of namespaces is used for creating a DNS search path which is propagated to
// the DNS-resolver in the root daemon each time an update arrives.
//
// The first update will close the firstSnapshotArrived channel.
func (kc *Cluster) StartNamespaceWatcher(ctx context.Context) {
	kc.namespaceWatcherSnapshot = make(map[string]struct{})
	nsSynced := make(chan struct{})
	go func() {
		api := kc.ki.CoreV1()
		for ctx.Err() == nil {
			w, err := api.Namespaces().Watch(ctx, meta.ListOptions{})
			if err != nil {
				dlog.Errorf(ctx, "unable to create service watcher: %v", err)
				return
			}
			kc.namespacesEventHandler(ctx, w.ResultChan(), nsSynced)
		}
	}()
	select {
	case <-ctx.Done():
	case <-nsSynced:
	}
}

func (kc *Cluster) namespacesEventHandler(ctx context.Context, evCh <-chan watch.Event, nsSynced chan struct{}) {
	// The delay timer will initially sleep forever. It's reset to a very short
	// delay when the file is modified.
	var delay *time.Timer
	delay = time.AfterFunc(time.Duration(math.MaxInt64), func() {
		kc.refreshNamespaces(ctx)
		select {
		case <-nsSynced:
		default:
			close(nsSynced)
		}
	})
	defer delay.Stop()

	for {
		select {
		case <-ctx.Done():
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

func (kc *Cluster) canI(ctx context.Context, ra *auth.ResourceAttributes) (bool, error) {
	authHandler := kc.ki.AuthorizationV1().SelfSubjectAccessReviews()
	review := auth.SelfSubjectAccessReview{Spec: auth.SelfSubjectAccessReviewSpec{ResourceAttributes: ra}}
	ar, err := authHandler.Create(ctx, &review, meta.CreateOptions{})
	if err == nil && ar.Status.Allowed {
		return true, nil
	}
	where := ""
	if ra.Namespace != "" {
		where = " in namespace " + ra.Namespace
	}
	if err != nil {
		err = fmt.Errorf(`unable to do "can-i %s %s%s": %v`, ra.Verb, ra.Resource, where, err)
		if ctx.Err() == nil {
			dlog.Error(ctx, err)
		}
	} else {
		dlog.Infof(ctx, `"can-i %s %s%s" is not allowed`, ra.Verb, ra.Resource, where)
	}
	return false, err
}

// CanWatchNamespaces answers the question if this client has the RBAC permissions necessary
// to watch namespaces. The answer is likely false when using a namespaces scoped installation.
func (kc *Cluster) CanWatchNamespaces(ctx context.Context) bool {
	ok, err := kc.canI(ctx, &auth.ResourceAttributes{
		Verb:     "watch",
		Resource: "namespaces",
	})
	return err == nil && ok
}

// canGetDefaultTrafficManagerService answers the question if this client has the RBAC permissions
// necessary to get the traffic-manager in the default namespace.
func (kc *Cluster) canGetDefaultTrafficManagerService(ctx context.Context) bool {
	ok, err := kc.canI(ctx, &auth.ResourceAttributes{
		Verb:      "get",
		Resource:  "services",
		Name:      "traffic-manager",
		Namespace: defaultManagerNamespace,
	})
	return err == nil && ok
}

// canAccessNS answers the question if this client has the RBAC permissions
// necessary to list and intercept workloads the namespace.
func (kc *Cluster) canAccessNS(ctx context.Context, namespace string) bool {
	authHandler := kc.ki.AuthorizationV1().SelfSubjectRulesReviews()
	review := auth.SelfSubjectRulesReview{Spec: auth.SelfSubjectRulesReviewSpec{Namespace: namespace}}
	rr, err := authHandler.Create(ctx, &review, meta.CreateOptions{})
	if err != nil {
		dlog.Errorf(ctx, `unable to do "can-i --list" on namespace %s`, namespace)
	}
	ras := []*auth.ResourceAttributes{
		{
			Resource: "services",
			Verb:     "list",
		},
		{
			Resource: "services",
			Verb:     "watch",
		},
	}
	for _, r := range []string{"deployments", "replicasets", "statefulsets"} {
		for _, v := range []string{"get", "watch", "list"} {
			ras = append(ras, &auth.ResourceAttributes{
				Group:    "apps",
				Resource: r,
				Verb:     v,
			})
		}
	}

	sliceMatch := func(vs []string, s string) bool {
		return slice.Contains(vs, "*") || slice.Contains(vs, s)
	}
	// canDo will just compare the group, verb, and resource property. We know that the namespace is correct, and
	// we don't care about names or sub-resources.
	canDo := func(ra *auth.ResourceAttributes) bool {
		for _, rule := range rr.Status.ResourceRules {
			if sliceMatch(rule.APIGroups, ra.Group) && sliceMatch(rule.Verbs, ra.Verb) && sliceMatch(rule.Resources, ra.Resource) {
				return true
			}
		}
		return false
	}
	for _, ra := range ras {
		if !canDo(ra) {
			dlog.Errorf(ctx, `client can't do %s %s/%s in namespace %s`, ra.Verb, ra.Group, ra.Resource, namespace)
			return false
		}
	}
	return true
}

func sortedStringSlicesEqual(as, bs []string) bool {
	if len(as) != len(bs) {
		return false
	}
	for i, a := range as {
		if a != bs[i] {
			return false
		}
	}
	return true
}

func (kc *Cluster) SetMappedNamespaces(c context.Context, namespaces []string) bool {
	sort.Strings(namespaces)
	if !sortedStringSlicesEqual(namespaces, kc.MappedNamespaces) {
		kc.MappedNamespaces = namespaces
		kc.refreshNamespaces(c)
		return true
	}
	return false
}

func (kc *Cluster) AddNamespaceListener(c context.Context, nsListener userd.NamespaceListener) {
	kc.nsLock.Lock()
	kc.namespaceListeners = append(kc.namespaceListeners, nsListener)
	kc.nsLock.Unlock()
	nsListener(c)
}

func (kc *Cluster) refreshNamespaces(c context.Context) {
	kc.nsLock.Lock()
	defer kc.nsLock.Unlock()
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
				accessOk = kc.canAccessNS(c, ns)
			}
			namespaces[ns] = accessOk
		}
	}
	equal := len(namespaces) == len(kc.currentMappedNamespaces)
	if equal {
		for k, ov := range kc.currentMappedNamespaces {
			if nv, ok := namespaces[k]; !ok || nv != ov {
				equal = false
				break
			}
		}
	}
	if equal {
		return
	}
	kc.currentMappedNamespaces = namespaces
	for _, nsListener := range kc.namespaceListeners {
		func() {
			kc.nsLock.Unlock()
			defer kc.nsLock.Lock()
			nsListener(c)
		}()
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
