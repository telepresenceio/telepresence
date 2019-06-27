// Package k8s is a facade over (super-terrible, very difficult to understand)
// client-go to provide a higher-level interface to Kubernetes, with support
// for simple, high-level APIs for watching resources (including from stable,
// long-running processes) and implementing basic controllers.
//
// It is intended to offer the same API for (nearly) every Kubernetes resource,
// including easy CRD access without codegen.
package k8s

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/google/shlex"
	"github.com/pkg/errors"
)

// KubeInfo holds the data required to talk to a cluster
type KubeInfo struct {
	Kubeconfig   string
	context      string
	namespace    string
	clientConfig clientcmd.ClientConfig
}

// NewKubeInfo returns a useable KubeInfo, handling optional
// kubeconfig, context, and namespace.
func NewKubeInfo(configfile, context, namespace string) *KubeInfo {
	// Find the correct kube config file
	configfilesearch := clientcmd.NewDefaultClientConfigLoadingRules()
	if len(configfile) != 0 {
		configfilesearch.ExplicitPath = configfile
	}

	// Possibly override context and namespace
	overrides := &clientcmd.ConfigOverrides{}
	if len(context) != 0 {
		overrides.CurrentContext = context
	}
	if len(namespace) != 0 {
		overrides.Context.Namespace = namespace
	}

	// Construct the config
	kubeconfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(configfilesearch, overrides)

	res := KubeInfo{
		configfile,
		context,
		namespace,
		kubeconfig,
	}

	return &res
}

// Context returns the context name of the KubeInfo.
func (info *KubeInfo) Context() (string, error) {
	// Extract context
	resultContext := info.context
	if len(info.context) == 0 {
		apiconfig, err := info.clientConfig.RawConfig()
		if err != nil {
			return "", err
		}
		resultContext = apiconfig.CurrentContext
	}
	return resultContext, nil
}

// Namespace returns the namespace for a KubeInfo.
func (info *KubeInfo) Namespace() (string, error) {
	// Extract namespace
	resultNamespace, _, err := info.clientConfig.Namespace()
	return resultNamespace, err
}

// GetRestConfig returns a REST config
func (info *KubeInfo) GetRestConfig() (*rest.Config, error) {
	/*
		// Do the right thing if you're running in a cluster
		if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
			inClusterConfig, err := rest.InClusterConfig()
			if err != nil {
				return nil, err
			}
			return inClusterConfig, nil
		}
	*/

	config, err := info.clientConfig.ClientConfig()
	if err != nil {
		return nil, err
	}
	return config, nil
}

// GetKubectl returns the arguments for a runnable kubectl command that talks to
// the same cluster as the associated ClientConfig.
func (info *KubeInfo) GetKubectl(args string) (string, error) {
	parts, err := shlex.Split(args)
	if err != nil {
		panic(err)
	}
	kargs, err := info.GetKubectlArray(parts...)
	if err != nil {
		return "", err
	}
	return strings.Join(kargs, " "), nil
}

// GetKubectlArray does what GetKubectl does but returns the result as a []string.
func (info *KubeInfo) GetKubectlArray(args ...string) ([]string, error) {
	res := []string{"kubectl"}
	if len(info.Kubeconfig) != 0 {
		res = append(res, "--kubeconfig", info.Kubeconfig)
	}
	context, err := info.Context()
	if err != nil {
		return nil, err
	}
	namespace, err := info.Namespace()
	if err != nil {
		return nil, err
	}
	res = append(res, "--context", context, "--namespace", namespace)
	res = append(res, args...)
	return res[1:], nil // Drop leading "kubectl" because reasons...
}

// Client is the top-level handle to the Kubernetes cluster.
type Client struct {
	config *rest.Config
	mapper meta.RESTMapper
}

// NewClient constructs a k8s.Client, optionally using a previously-constructed
// KubeInfo.
func NewClient(info *KubeInfo) (*Client, error) {
	if info == nil {
		info = NewKubeInfo("", "", "") // Empty file/ctx/ns for defaults
	}
	config, err := info.GetRestConfig()
	if err != nil {
		return nil, errors.Errorf("Failed to get REST config: %v", err)
	}

	disco, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}

	resources, err := restmapper.GetAPIGroupResources(disco)
	if err != nil {
		return nil, err
	}

	return &Client{
		config: config,
		mapper: restmapper.NewShortcutExpander(restmapper.NewDiscoveryRESTMapper(resources), disco),
	}, nil
}

// ResourceType describes a Kubernetes resource type in a particular cluster.
// See ResolveResourceType() for more information.
//
// It is equivalent to a k8s.io/apimachinery/pkg/api/meta.RESTMapping
type ResourceType struct {
	Group      string
	Version    string
	Name       string // lowercase plural, called Resource in kubernetes code
	Kind       string // uppercase singular
	Namespaced bool
}

func (r ResourceType) String() string {
	return r.Name + "." + r.Version + "." + r.Group
}

// ResolveResourceType takes the name of a resource type (singular,
// plural, or an abbreviation; like you might pass to `kubectl get`)
// and returns cluster-specific canonical information about that
// resource type.
//
// For example, with Kubernetes v1.10.5:
//   "pod"        --> {Group: "",           Version: "v1",      Name: "pods",        Kind: "Pod",        Namespaced: true}
//   "deployment" --> {Group: "extensions", Version: "v1beta1", Name: "deployments", Kind: "Deployment", Namespaced: true}
//
// Newer versions of Kubernetes might instead put "pod" in the "core"
// group, or put "deployment" in apps/v1 instead of
// extensions/v1beta1.  Because of discrepancies between different
// clusters, it may be a good idea to use this even for internal
// callers, rather than treating it purely as a UI concern.
//
// This implementation is supposed to be equivalent to
// k8s.io/cli-runtime/pkg/genericclioptions/resource.Builder.mappingFor(),
// which calls
// k8s.io/apimachinery/pkg/runtime/schema.ParseResourceArg() and
// k8s.io/client-go/restmapper.shortcutExpander.expandResourceShortcut()
func (c *Client) ResolveResourceType(resource string) (ResourceType, error) {
	fullySpecifiedGVR, groupResource := schema.ParseResourceArg(resource)
	gvk := schema.GroupVersionKind{}

	if fullySpecifiedGVR != nil {
		gvk, _ = c.mapper.KindFor(*fullySpecifiedGVR)
	}
	if gvk.Empty() {
		gvk, _ = c.mapper.KindFor(groupResource.WithVersion(""))
	}
	if !gvk.Empty() {
		return wrapRESTMapping(c.mapper.RESTMapping(gvk.GroupKind(), gvk.Version))
	}

	fullySpecifiedGVK, groupKind := schema.ParseKindArg(resource)
	if fullySpecifiedGVK == nil {
		gvk := groupKind.WithVersion("")
		fullySpecifiedGVK = &gvk
	}

	if !fullySpecifiedGVK.Empty() {
		if mapping, err := c.mapper.RESTMapping(fullySpecifiedGVK.GroupKind(), fullySpecifiedGVK.Version); err == nil {
			return wrapRESTMapping(mapping, nil)
		}
	}

	mapping, err := c.mapper.RESTMapping(groupKind, gvk.Version)
	if err != nil {
		// if we error out here, it is because we could not match a resource or a kind
		// for the given argument. To maintain consistency with previous behavior,
		// announce that a resource type could not be found.
		// if the error is _not_ a *meta.NoKindMatchError, then we had trouble doing discovery,
		// so we should return the original error since it may help a user diagnose what is actually wrong
		if meta.IsNoMatchError(err) {
			return ResourceType{}, fmt.Errorf("the server doesn't have a resource type %q", groupResource.Resource)
		}
		return ResourceType{}, err
	}
	return wrapRESTMapping(mapping, err)
}

func wrapRESTMapping(m *meta.RESTMapping, err error) (ResourceType, error) {
	if err != nil || m == nil {
		return ResourceType{}, err
	}

	return ResourceType{
		Group:      m.GroupVersionKind.Group,
		Version:    m.GroupVersionKind.Version,
		Name:       m.Resource.Resource,
		Kind:       m.GroupVersionKind.Kind,
		Namespaced: m.Scope.Name() == meta.RESTScopeNameNamespace,
	}, nil
}

// List calls ListNamespace(...) with the empty string as the namespace, which
// means all namespaces if the resource is namespaced.
func (c *Client) List(resource string) ([]Resource, error) {
	return c.ListNamespace("", resource)
}

// ListNamespace returns a slice of Resources.
// If the resource is not namespaced, the namespace must be the empty string.
// If the resource is namespaced, the empty string lists across all namespaces.
func (c *Client) ListNamespace(namespace, resource string) ([]Resource, error) {
	return c.SelectiveList(namespace, resource, "", "")
}

func (c *Client) SelectiveList(namespace, resource, fieldSelector, labelSelector string) ([]Resource, error) {
	return c.ListQuery(Query{
		Kind:          resource,
		Namespace:     namespace,
		FieldSelector: fieldSelector,
		LabelSelector: labelSelector,
	})
}

// Query describes a query for a set of kubernetes resources.
//
// The Kind of a query may use any of the short names or abbreviations
// permitted by kubectl.
//
// If the Namespace field is the empty string, then all namespaces
// will be queried.
//
// The FieldSelector and LabelSelector fields contain field and label
// selectors as documented by kubectl.
type Query struct {
	Kind          string
	Namespace     string
	FieldSelector string
	LabelSelector string
	resourceType  ResourceType
}

func (q *Query) resolve(c *Client) error {
	if q.resourceType != (ResourceType{}) {
		return nil
	}

	rt, err := c.ResolveResourceType(q.Kind)
	if err != nil {
		return err
	}
	q.resourceType = rt
	return nil
}

// ListQuery returns all the kubernetes resources that satisfy the
// supplied query.
func (c *Client) ListQuery(query Query) ([]Resource, error) {
	err := query.resolve(c)
	if err != nil {
		return nil, err
	}

	ri := query.resourceType

	dyn, err := dynamic.NewForConfig(c.config)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create dynamic context")
	}

	cli := dyn.Resource(schema.GroupVersionResource{
		Group:    ri.Group,
		Version:  ri.Version,
		Resource: ri.Name,
	})

	var filtered dynamic.ResourceInterface
	if query.Namespace != "" {
		filtered = cli.Namespace(query.Namespace)
	} else {
		filtered = cli
	}

	uns, err := filtered.List(v1.ListOptions{
		FieldSelector: query.FieldSelector,
		LabelSelector: query.LabelSelector,
	})
	if err != nil {
		return nil, err
	}

	result := make([]Resource, len(uns.Items))
	for idx, un := range uns.Items {
		result[idx] = un.UnstructuredContent()
	}
	return result, nil
}
