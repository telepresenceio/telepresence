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

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/google/shlex"
	"github.com/pkg/errors"
)

// KubeInfo holds the data required to talk to a cluster
type KubeInfo struct {
	Kubeconfig   string
	Context      string
	Namespace    string
	clientConfig clientcmd.ClientConfig
}

// NewKubeInfo returns a useable KubeInfo, handling optional
// kubeconfig, context, and namespace.
func NewKubeInfo(configfile, context, namespace string) (*KubeInfo, error) {
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

	// Extract resulting context and namespace
	resultContext := context
	if len(context) == 0 {
		apiconfig, err := kubeconfig.RawConfig()
		if err != nil {
			return nil, err
		}
		resultContext = apiconfig.CurrentContext
	}

	resultNamespace, _, err := kubeconfig.Namespace()
	if err != nil {
		return nil, err
	}

	res := KubeInfo{
		configfile,
		resultContext,
		resultNamespace,
		kubeconfig,
	}

	return &res, nil
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
func (info *KubeInfo) GetKubectl(args string) string {
	parts, err := shlex.Split(args)
	if err != nil {
		panic(err)
	}
	return strings.Join(info.GetKubectlArray(parts...), " ")
}

func (info *KubeInfo) GetKubectlArray(args ...string) []string {
	res := []string{"kubectl"}
	if len(info.Kubeconfig) != 0 {
		res = append(res, "--kubeconfig", info.Kubeconfig)
	}
	res = append(res, "--context", info.Context, "--namespace", info.Namespace)
	res = append(res, args...)
	return res[1:] // Drop leading "kubectl" because reasons...
}

// Client is the top-level handle to the Kubernetes cluster.
type Client struct {
	config    *rest.Config
	resources []*v1.APIResourceList
}

// NewClient constructs a k8s.Client, optionally using a previously-constructed
// KubeInfo.
func NewClient(info *KubeInfo) *Client {
	if info == nil {
		var err error
		info, err = NewKubeInfo("", "", "") // Empty file/ctx/ns for defaults
		if err != nil {
			panic(err)
		}
	}
	config, err := info.GetRestConfig()
	if err != nil {
		panic(fmt.Sprintf("Failed to get REST config: %v", err))
	}

	disco, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		panic(err)
	}

	resources, err := disco.ServerResources()
	if err != nil {
		panic(err)
	}

	return &Client{
		config:    config,
		resources: resources,
	}
}

// ResourceType describes a Kubernetes resource type in a particular cluster.
// See ResolveResourceType() for more information.
//
// It is equivalent to a k8s.io/apimachinery/pkg/api/meta.RESTMapping
type ResourceType struct {
	Group      string
	Version    string
	Name       string // lowercase plural
	Kind       string // uppercase singular
	Namespaced bool
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
// BUG(lukeshu): ResolveResourceType currently only takes the type name, it should
// accept TYPE[[.VERSION].GROUP], like `kubectl`.
//
// BUG(lukeshu): ResolveResourceType currently returns the first
// match.  In the event of multiple resource types with the same name
// (multiple API groups, multiple versions), it should do something
// more intelligent than that; it should at least pay attention to the
// API group's PreferredVersion.
//
// Should be equivalent to
// k8s.io/cli-runtime/pkg/genericclioptions/resource.Builder.mappingFor(),
// which calls
// k8s.io/apimachinery/pkg/runtime/schema.ParseResourceArg() and
// k8s.io/client-go/restmapper.shortcutExpander.expandResourceShortcut()
func (c *Client) ResolveResourceType(resource string) (ResourceType, error) {
	if resource == "" {
		return ResourceType{}, errors.New("empty resource string")
	}
	lresource := strings.ToLower(resource)
	for _, rl := range c.resources {
		for _, r := range rl.APIResources {
			candidates := []string{
				r.Name,         // lowercase plural
				r.Kind,         // uppercase singular
				r.SingularName, // lowercase singular
			}
			candidates = append(candidates, r.ShortNames...)

			for _, c := range candidates {
				if lresource == strings.ToLower(c) {
					var group string
					var version string
					parts := strings.Split(rl.GroupVersion, "/")
					switch len(parts) {
					case 1:
						group = ""
						version = parts[0]
					case 2:
						group = parts[0]
						version = parts[1]
					default:
						return ResourceType{}, errors.New("unrecognized GroupVersion")
					}
					return ResourceType{group, version, r.Name, r.Kind, r.Namespaced}, nil
				}
			}
		}
	}
	return ResourceType{}, errors.New(fmt.Sprintf("unrecognized resource: %s", resource))
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
	ri, err := c.ResolveResourceType(resource)
	if err != nil {
		return nil, err
	}

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
	if namespace != "" {
		filtered = cli.Namespace(namespace)
	} else {
		filtered = cli
	}

	uns, err := filtered.List(v1.ListOptions{
		FieldSelector: fieldSelector,
		LabelSelector: labelSelector,
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
