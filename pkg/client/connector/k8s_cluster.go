package connector

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/pkg/errors"

	"github.com/datawire/telepresence2/pkg/client"
)

const connectTimeout = 5 * time.Second

// k8sCluster is a Kubernetes cluster reference
type k8sCluster struct {
	kates.ClientOptions
	client *kates.Client
	srv    string
	kargs  []string
}

// getKubectlArgs returns the kubectl command arguments to run a
// kubectl command with this cluster.
func (kc *k8sCluster) getKubectlArgs(args ...string) []string {
	if kc.Kubeconfig != "" {
		args = append(args, "--kubeconfig", kc.Kubeconfig)
	}
	if kc.Context != "" {
		args = append(args, "--context", kc.Context)
	}
	if kc.Namespace != "" {
		args = append(args, "--namespace", kc.Namespace)
	}
	return append(args, kc.kargs...)
}

// getKubectlCmd returns a Cmd that runs kubectl with the given arguments and
// the appropriate environment to talk to the cluster
func (kc *k8sCluster) getKubectlCmd(c context.Context, args ...string) *dexec.Cmd {
	return dexec.CommandContext(c, "kubectl", kc.getKubectlArgs(args...)...)
}

// server returns the cluster's server configuration
func (kc *k8sCluster) server() string {
	return kc.srv
}

func (kc *k8sCluster) portForwardAndThen(c context.Context, kpfArgs []string, thenName string, then func(context.Context) error) error {
	pf := dexec.CommandContext(c, "kubectl", kc.getKubectlArgs(kpfArgs...)...)
	out, err := pf.StdoutPipe()
	if err != nil {
		return err
	}

	// We want this command to keep on running. If it returns an error, then it was unsuccessful.
	if err = pf.Start(); err != nil {
		out.Close()
		dlog.Errorf(c, "port-forward failed to start: %v", client.RunError(err))
		return err
	}

	sc := bufio.NewScanner(out)

	// Give port-forward 10 seconds to produce the correct output and spawn the next process
	timer := time.AfterFunc(10*time.Second, func() {
		_ = pf.Process.Kill()
	})

	// wait group is done when next process starts.
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer out.Close()
		ok := false
		for sc.Scan() {
			txt := sc.Text()
			if !ok && strings.HasPrefix(txt, "Forwarding from") {
				// Forwarding is running. This is what we waited for
				dlog.Debug(c, txt)
				ok = true
				timer.Stop()
				wg.Done()
				dgroup.ParentGroup(c).Go(thenName, then)
			}
		}

		// let the port forward continue running. It will either be killed by the
		// timer (if it didn't produce the expected output) or by a context cancel.
		if err = pf.Wait(); err != nil {
			if c.Err() != nil {
				// Context cancelled
				err = nil
			} else {
				err = client.RunError(err)
				dlog.Errorf(c, "port-forward failed: %v", err)
			}
		}
		if !ok {
			timer.Stop()
			wg.Done()
		}
	}()

	// Wait for successful start of next process or failure to do port-forward
	wg.Wait()
	return err
}

// check for cluster connectivity
func (kc *k8sCluster) check(c context.Context) error {
	c, cancel := context.WithTimeout(c, connectTimeout)
	defer cancel()
	err := kc.getKubectlCmd(c, "get", "po", "ohai", "--ignore-not-found").Run()
	if err != nil {
		if c.Err() == context.DeadlineExceeded {
			err = errors.New("timeout when testing cluster connectivity")
		}
	}
	return err
}

func newKCluster(kubeConfig, ctxName, namespace string, kargs ...string) (*k8sCluster, error) {
	opts := kates.ClientOptions{
		Kubeconfig: kubeConfig,
		Context:    ctxName,
		Namespace:  namespace}

	kc, err := kates.NewClient(opts)
	if err != nil {
		return nil, err
	}
	return &k8sCluster{ClientOptions: opts, client: kc, kargs: kargs}, nil
}

// trackKCluster tracks connectivity to a cluster
func trackKCluster(c context.Context, ctxName, namespace string, kargs []string) (*k8sCluster, error) {
	// TODO: All shell-outs to kubectl here should go through the kates client.
	if ctxName == "" {
		cmd := dexec.CommandContext(c, "kubectl", "config", "current-context")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("kubectl config current-context: %v", client.RunError(err))
		}
		ctxName = strings.TrimSpace(string(output))
	}

	if namespace == "" {
		nsQuery := fmt.Sprintf("jsonpath={.contexts[?(@.name==\"%s\")].context.namespace}", ctxName)
		cmd := dexec.CommandContext(c, "kubectl", "--context", ctxName, "config", "view", "-o", nsQuery)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("kubectl config view ns failed: %v", client.RunError(err))
		}
		namespace = strings.TrimSpace(string(output))
		if namespace == "" { // This is what kubens does
			namespace = "default"
		}
	}

	kc, err := newKCluster("", ctxName, namespace, kargs...)
	if err != nil {
		return nil, fmt.Errorf("k8s client create failed. %v", client.RunError(err))
	}

	if err := kc.check(c); err != nil {
		return nil, fmt.Errorf("initial cluster check failed: %v", client.RunError(err))
	}
	dlog.Infof(c, "Context: %s", kc.Context)

	cmd := kc.getKubectlCmd(c, "config", "view", "--minify", "-o", "jsonpath={.clusters[0].cluster.server}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("kubectl config view server: %v", client.RunError(err))
	}
	kc.srv = strings.TrimSpace(string(output))
	dlog.Infof(c, "Server: %s", kc.srv)
	return kc, nil
}

/*
// getClusterPreviewHostname returns the hostname of the first Host resource it
// finds that has Preview URLs enabled with a supported URL type.
func (c *k8sCluster) getClusterPreviewHostname(ctx context.Context) (string, error) {
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
