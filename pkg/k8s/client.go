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

// GetKubectl returns the arguents for a runnable kubectl command
func (info *KubeInfo) GetKubectl(args string) string {
	res := []string{"kubectl"}
	if len(info.Kubeconfig) != 0 {
		res = append(res, "--kubeconfig", info.Kubeconfig)
	}
	res = append(res, "--context", info.Context, "--namespace", info.Namespace)
	res = append(res, args)
	return strings.Join(res[1:], " ") // Drop leading "kubectl" because reasons...
}

type Client struct {
	config    *rest.Config
	resources []*v1.APIResourceList
}

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

type ResourceInfo struct {
	Group      string
	Version    string
	Name       string
	Kind       string
	Namespaced bool
}

func (c *Client) resolve(resource string) ResourceInfo {
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
					return ResourceInfo{group, version, r.Name, r.Kind, r.Namespaced}
				}
			}
		}
	}
	panic(fmt.Sprintf("unrecognized resource: %s", resource))
}

func (c *Client) List(resource string) ([]Resource, error) {
	ri := c.resolve(resource)

	dyn, err := dynamic.NewForConfig(c.config)
	if err != nil {
		log.Fatal(err)
	}

	cli := dyn.Resource(schema.GroupVersionResource{
		Group:    ri.Group,
		Version:  ri.Version,
		Resource: ri.Name,
	})

	uns, err := cli.List(v1.ListOptions{})
	if err != nil {
		return nil, err
	}

	result := make([]Resource, len(uns.Items))
	for idx, un := range uns.Items {
		result[idx] = un.UnstructuredContent()
	}
	return result, nil
}
