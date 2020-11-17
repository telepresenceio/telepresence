package connector

import (
	"fmt"
	"strings"

	"github.com/datawire/ambassador/pkg/kates"

	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/pkg/errors"

	"github.com/datawire/telepresence2/pkg/client"
)

// k8sCluster is a Kubernetes cluster reference
type k8sCluster struct {
	kates.ClientOptions
	client       *kates.Client
	srv          string
	kargs        []string
	isBridgeOkay func() bool
	client.ResourceBase
}

// getKubectlArgs returns the kubectl command arguments to run a
// kubectl command with this cluster.
func (c *k8sCluster) getKubectlArgs(args ...string) []string {
	if c.Kubeconfig != "" {
		args = append(args, "--kubeconfig", c.Kubeconfig)
	}
	if c.Context != "" {
		args = append(args, "--context", c.Context)
	}
	if c.Namespace != "" {
		args = append(args, "--namespace", c.Namespace)
	}
	return append(args, c.kargs...)
}

// getKubectlCmd returns a Cmd that runs kubectl with the given arguments and
// the appropriate environment to talk to the cluster
func (c *k8sCluster) getKubectlCmd(p *supervisor.Process, args ...string) *supervisor.Cmd {
	return p.Command("kubectl", c.getKubectlArgs(args...)...)
}

// server returns the cluster's server configuration
func (c *k8sCluster) server() string {
	return c.srv
}

// setBridgeCheck sets the callable used to check whether the Teleproxy bridge
// is functioning. If this is nil/unset, cluster monitoring checks the cluster
// directly (via kubectl)
func (c *k8sCluster) setBridgeCheck(isBridgeOkay func() bool) {
	c.isBridgeOkay = isBridgeOkay
}

// check for cluster connectivity
func (c *k8sCluster) check(p *supervisor.Process) error {
	// If the bridge is okay then the cluster is okay
	if c.isBridgeOkay != nil && c.isBridgeOkay() {
		return nil
	}
	cmd := c.getKubectlCmd(p, "get", "po", "ohai", "--ignore-not-found")
	return cmd.Run()
}

func newKCluster(kubeConfig, context, namespace string, kargs ...string) (*k8sCluster, error) {
	opts := kates.ClientOptions{
		Kubeconfig: kubeConfig,
		Context:    context,
		Namespace:  namespace}

	kc, err := kates.NewClient(opts)
	if err != nil {
		return nil, err
	}
	return &k8sCluster{ClientOptions: opts, client: kc, kargs: kargs}, nil
}

// trackKCluster tracks connectivity to a cluster
func trackKCluster(p *supervisor.Process, context, namespace string, kargs []string) (*k8sCluster, error) {
	// TODO: All shell-outs to kubectl here should go through the kates client.
	if context == "" {
		cmd := p.Command("kubectl", "config", "current-context")
		p.Logf("%s %v", cmd.Path, cmd.Args[1:])
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, errors.Wrap(err, "kubectl config current-context")
		}
		context = strings.TrimSpace(string(output))
	}

	if namespace == "" {
		nsQuery := fmt.Sprintf("jsonpath={.contexts[?(@.name==\"%s\")].context.namespace}", context)
		cmd := p.Command("kubectl", "--context", context, "config", "view", "-o", nsQuery)
		p.Logf("%s %v", cmd.Path, cmd.Args[1:])
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, errors.Wrap(err, "kubectl config view ns")
		}
		namespace = strings.TrimSpace(string(output))
		if namespace == "" { // This is what kubens does
			namespace = "default"
		}
	}

	c, err := newKCluster("", context, namespace, kargs...)
	if err != nil {
		return nil, errors.Wrap(err, "k8s client create")
	}

	if err := c.check(p); err != nil {
		return nil, errors.Wrap(err, "initial cluster check")
	}
	p.Logf("Context: %s", c.Context)

	cmd := c.getKubectlCmd(p, "config", "view", "--minify", "-o", "jsonpath={.clusters[0].cluster.server}")
	p.Logf("%s %v", cmd.Path, cmd.Args[1:])
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, errors.Wrap(err, "kubectl config view server")
	}
	c.srv = strings.TrimSpace(string(output))
	p.Logf("Server: %s", c.srv)

	c.Setup(p.Supervisor(), "cluster", c.check, func(p *supervisor.Process) error { c.SetDone(); return nil })
	return c, nil
}

/*
// getClusterPreviewHostname returns the hostname of the first Host resource it
// finds that has Preview URLs enabled with a supported URL type.
func (c *k8sCluster) getClusterPreviewHostname(p *supervisor.Process) (string, error) {
	p.Log("Looking for a Host with Preview URLs enabled")

	// kubectl get hosts, in all namespaces or in this namespace
	outBytes, err := func() ([]byte, error) {
		clusterCmd := c.getKubectlCmdNoNamespace(p, "get", "host", "-o", "yaml", "--all-namespaces")
		if outBytes, err := clusterCmd.CombinedOutput(); err == nil {
			return outBytes, nil
		}
		return c.getKubectlCmd(p, "get", "host", "-o", "yaml").CombinedOutput()
	}()
	if err != nil {
		return "", err
	}

	// Parse the output
	hostLists, err := k8s.ParseResources("get hosts", string(outBytes))
	if err != nil {
		return "", err
	}
	if len(hostLists) != 1 {
		return "", errors.Errorf("weird result with length %d", len(hostLists))
	}

	// Grab the "items" slice, as the result should be a list of Host resources
	hostItems := k8s.Map(hostLists[0]).GetMaps("items")
	p.Logf("Found %d Host resources", len(hostItems))

	// Loop over Hosts looking for a Preview URL hostname
	for _, hostItem := range hostItems {
		host := k8s.Resource(hostItem)
		logEntry := fmt.Sprintf("- Host %s / %s: %%s", host.Namespace(), host.Name())

		previewURLSpec := host.Spec().GetMap("previewUrl")
		if len(previewURLSpec) == 0 {
			p.Logf(logEntry, "no preview URL teleproxy")
			continue
		}

		if enabled, ok := previewURLSpec["enabled"].(bool); !ok || !enabled {
			p.Logf(logEntry, "preview URL not enabled")
			continue
		}

		// missing type, default is "Path" --> success
		// type is present, set to "Path" --> success
		// otherwise --> failure
		if pType, ok := previewURLSpec["type"].(string); ok && pType != "Path" {
			p.Logf(logEntry+": %#v", "unsupported preview URL type", previewURLSpec["type"])
			continue
		}

		var hostname string
		if hostname = host.Spec().GetString("hostname"); hostname == "" {
			p.Logf(logEntry, "empty hostname???")
			continue
		}

		p.Logf(logEntry+": %q", "SUCCESS! Hostname is", hostname)
		return hostname, nil
	}

	p.Logf("No appropriate Host resource found.")
	return "", nil
}
*/
