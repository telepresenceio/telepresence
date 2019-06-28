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

	restMapper      meta.RESTMapper
	discoveryClient discovery.DiscoveryInterface
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

	// TODO(lukeshu): Optionally use a DiscoveryClient that does kubectl-like filesystem
	// caching; see k8s.io/cli-runtime/pkg/genericclioptions.ConfigFlags.ToDiscoveryClient().
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}

	// TODO(lukeshu): Use a *restmapper.DeferredDiscoveryRESTMapper to lazily call
	// restmapper.GetAPIGroupResources().  This is blocked by discoveryClient implementing
	// discovery.DiscoveryInterface but not discovery.CachedDiscoveryInterface (probably
	// resolved with the above TODO).
	resources, err := restmapper.GetAPIGroupResources(discoveryClient)
	if err != nil {
		return nil, err
	}

	return &Client{
		config: config,

		restMapper:      restmapper.NewDiscoveryRESTMapper(resources),
		discoveryClient: discoveryClient,
	}, nil
}

// ResourceType describes a Kubernetes resource type in a particular cluster.
// See ResolveResourceType() for more information.
//
// It is equivalent to a k8s.io/apimachinery/pkg/api/meta.RESTMapping
type ResourceType struct {
	Group      string
	Version    string
	Name       string // lowercase plural, called Resource in Kubernetes code
	Kind       string // uppercase singular
	Namespaced bool
}

func (r ResourceType) String() string {
	return r.Name + "." + r.Version + "." + r.Group
}

// ResolveResourceType takes the name of a resource type
// (TYPE[[.VERSION].GROUP], where TYPE may be singular, plural, or an
// abbreviation; like you might pass to `kubectl get`) and returns
// cluster-specific canonical information about that resource type.
//
// For example, with Kubernetes v1.10.5:
//  "pod"        -> {Group: "",           Version: "v1",      Name: "pods",        Kind: "Pod",        Namespaced: true}
//  "deployment" -> {Group: "extensions", Version: "v1beta1", Name: "deployments", Kind: "Deployment", Namespaced: true}
//  "svc.v1."    -> {Group: "",           Version: "v1",      Name: "services",    Kind: "Service",    Namespaced: true}
//
// Newer versions of Kubernetes might instead put "pod" in the "core"
// group, or put "deployment" in apps/v1 instead of
// extensions/v1beta1.
func (c *Client) ResolveResourceType(resource string) (ResourceType, error) {
	shortcutExpander := restmapper.NewShortcutExpander(c.restMapper, c.discoveryClient)
	restmapping, err := mappingFor(resource, shortcutExpander)
	if err != nil {
		return ResourceType{}, err
	}
	return ResourceType{
		Group:      restmapping.GroupVersionKind.Group,
		Version:    restmapping.GroupVersionKind.Version,
		Name:       restmapping.Resource.Resource,
		Kind:       restmapping.GroupVersionKind.Kind,
		Namespaced: restmapping.Scope.Name() == meta.RESTScopeNameNamespace,
	}, nil
}

// mappingFor returns the RESTMapping for the Kind given, or the Kind referenced by the resource.
// Prefers a fully specified GroupVersionResource match. If one is not found, we match on a fully
// specified GroupVersionKind, or fallback to a match on GroupKind.
//
// This is copy/pasted from k8s.io/cli-runtime/pkg/resource.Builder.mappingFor() (which is
// unfortunately private), with modified lines marked with "// MODIFIED".
func mappingFor(resourceOrKindArg string, restMapper meta.RESTMapper) (*meta.RESTMapping, error) { // MODIFIED: args
	fullySpecifiedGVR, groupResource := schema.ParseResourceArg(resourceOrKindArg)
	gvk := schema.GroupVersionKind{}
	// MODIFIED: Don't call b.restMapperFn(), use the mapper given as an argument.

	if fullySpecifiedGVR != nil {
		gvk, _ = restMapper.KindFor(*fullySpecifiedGVR)
	}
	if gvk.Empty() {
		gvk, _ = restMapper.KindFor(groupResource.WithVersion(""))
	}
	if !gvk.Empty() {
		return restMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	}

	fullySpecifiedGVK, groupKind := schema.ParseKindArg(resourceOrKindArg)
	if fullySpecifiedGVK == nil {
		gvk := groupKind.WithVersion("")
		fullySpecifiedGVK = &gvk
	}

	if !fullySpecifiedGVK.Empty() {
		if mapping, err := restMapper.RESTMapping(fullySpecifiedGVK.GroupKind(), fullySpecifiedGVK.Version); err == nil {
			return mapping, nil
		}
	}

	mapping, err := restMapper.RESTMapping(groupKind, gvk.Version)
	if err != nil {
		// if we error out here, it is because we could not match a resource or a kind
		// for the given argument. To maintain consistency with previous behavior,
		// announce that a resource type could not be found.
		// if the error is _not_ a *meta.NoKindMatchError, then we had trouble doing discovery,
		// so we should return the original error since it may help a user diagnose what is actually wrong
		if meta.IsNoMatchError(err) {
			return nil, fmt.Errorf("the server doesn't have a resource type %q", groupResource.Resource)
		}
		return nil, err
	}

	return mapping, nil
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

// Query describes a query for a set of Kubernetes resources.
type Query struct {
	// The Kind of a query may use any of the short names or abbreviations permitted by kubectl.
	Kind string

	// The Namespace field specifies the namespace to query.  If this field is the empty string,
	// then all namespaces will be queried.
	Namespace string

	// The FieldSelector and LabelSelector fields contain field and label selectors as
	// documented by kubectl.
	FieldSelector string
	LabelSelector string

	resourceType ResourceType
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

// ListQuery returns all the Kubernetes resources that satisfy the
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
