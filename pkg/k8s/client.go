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
	"log"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

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

// GetKubectl returns the arguents for a runnable kubectl command that talks to
// the same cluster as the associated ClientConfig.
func (info *KubeInfo) GetKubectl(args string) string {
	res := []string{"kubectl"}
	if len(info.Kubeconfig) != 0 {
		res = append(res, "--kubeconfig", info.Kubeconfig)
	}
	res = append(res, "--context", info.Context, "--namespace", info.Namespace)
	res = append(res, args)
	return strings.Join(res[1:], " ") // Drop leading "kubectl" because reasons...
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
			log.Fatal(err)
		}
	}
	config, err := info.GetRestConfig()
	if err != nil {
		log.Fatalln("Failed to get REST config:", err)
	}

	disco, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		log.Fatal(err)
	}

	resources, err := disco.ServerResources()
	if err != nil {
		log.Fatal(err)
	}

	return &Client{
		config:    config,
		resources: resources,
	}
}

// resourceInfo describes a Kubernetes resource type in a particular cluster.
// See resolve() for more information.
type resourceInfo struct {
	Group      string
	Version    string
	Name       string // lowercase plural
	Kind       string // uppercase singular
	Namespaced bool
}

// resolve takes a specially-formatted string (like you might pass to kubectl
// get) and returns cluster-specific canonical information about that resource
// type. E.g.,
//   "pod" --> {"", "v1", "pods", "Pod", true}
//   "deployment" --> {"extensions", "v1beta1", "deployments", "Deployment", true}
func (c *Client) resolve(resource string) resourceInfo {
	if resource == "" {
		panic("empty resource string")
	}
	lresource := strings.ToLower(resource)
	for _, rl := range c.resources {
		for _, r := range rl.APIResources {
			candidates := []string{
				r.Name,
				r.Kind,
				r.SingularName,
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
						panic("unrecognized GroupVersion")
					}
					return resourceInfo{group, version, r.Name, r.Kind, r.Namespaced}
				}
			}
		}
	}
	panic(fmt.Sprintf("unrecognized resource: %s", resource))
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
	ri := c.resolve(resource)

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

	uns, err := filtered.List(v1.ListOptions{})
	if err != nil {
		return nil, err
	}

	result := make([]Resource, len(uns.Items))
	for idx, un := range uns.Items {
		result[idx] = un.UnstructuredContent()
	}
	return result, nil
}
