package connector

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/pflag"
	v1 "k8s.io/api/core/v1"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dutil"
	"github.com/datawire/telepresence2/rpc/v2/daemon"
	"github.com/datawire/telepresence2/v2/pkg/client"
)

const connectTimeout = 5 * time.Second

// k8sCluster is a Kubernetes cluster reference
type k8sCluster struct {
	// Things for bypassing kates
	Server       string
	Context      string
	Namespace    string
	kubeFlagArgs []string

	// Main
	client *kates.Client
	daemon daemon.DaemonClient // for DNS updates

	// Current snapshot.
	// These fields (except for accLock/accCond) get set by acc.Update().
	accLock     sync.Mutex
	accCond     *sync.Cond
	Deployments []*kates.Deployment
	Pods        []*kates.Pod
	Services    []*kates.Service
}

// getKubectlArgs returns the kubectl command arguments to run a
// kubectl command with this cluster.
func (kc *k8sCluster) getKubectlArgs(args ...string) []string {
	return append(kc.kubeFlagArgs, args...)
}

// getKubectlCmd returns a Cmd that runs kubectl with the given arguments and
// the appropriate environment to talk to the cluster
func (kc *k8sCluster) getKubectlCmd(c context.Context, args ...string) *dexec.Cmd {
	return dexec.CommandContext(c, "kubectl", kc.getKubectlArgs(args...)...)
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
	cmd := kc.getKubectlCmd(c, "get", "po", "ohai", "--ignore-not-found")
	stderr := bytes.Buffer{}
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if c.Err() == context.DeadlineExceeded {
			err = errors.New("timeout when testing cluster connectivity")
		} else if msg := stderr.String(); len(msg) > 0 {
			err = errors.New(msg)
		}
	}
	return err
}

func (kc *k8sCluster) createWatch(c context.Context, namespace string) (acc *kates.Accumulator, err error) {
	defer func() {
		if r := dutil.PanicToError(recover()); r != nil {
			err = r
		}
	}()

	return kc.client.Watch(c,
		kates.Query{
			Name:      "Services",
			Namespace: namespace,
			Kind:      "service",
		},
		kates.Query{
			Name:      "Deployments",
			Namespace: namespace,
			Kind:      "deployment",
		},
		kates.Query{
			Name:      "Pods",
			Namespace: namespace,
			Kind:      "pod",
		}), nil
}

func (kc *k8sCluster) waitUntil(ctx context.Context, fn func(context.Context) (bool, error)) error {
	kc.accLock.Lock()
	defer kc.accLock.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		done, err := fn(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		kc.accCond.Wait()
	}
}

func (kc *k8sCluster) startWatches(c context.Context, namespace string, accWait chan<- struct{}) error {
	acc, err := kc.createWatch(c, namespace)
	if err != nil {
		return err
	}

	closeAccWait := func() {
		if accWait != nil {
			close(accWait)
			accWait = nil
		}
	}

	dgroup.ParentGroup(c).Go("watch-k8s", func(c context.Context) error {
		for {
			select {
			case <-c.Done():
				closeAccWait()
				return nil
			case <-acc.Changed():
				func() {
					kc.accLock.Lock()
					defer kc.accLock.Unlock()
					if acc.Update(kc) {
						kc.updateTable(c)
						kc.accCond.Broadcast()
					}
				}()
				closeAccWait()
			}
		}
	})
	return nil
}

func (kc *k8sCluster) updateTable(c context.Context) {
	if kc.daemon == nil {
		return
	}

	table := daemon.Table{Name: "kubernetes"}
	for _, svc := range kc.Services {
		spec := svc.Spec

		ip := spec.ClusterIP
		// for headless services the IP is None, we
		// should properly handle these by listening
		// for endpoints and returning multiple A
		// records at some point
		if ip == "" || ip == "None" {
			continue
		}
		qName := svc.Name + "." + svc.Namespace + ".svc.cluster.local"

		ports := ""
		for _, port := range spec.Ports {
			if ports == "" {
				ports = fmt.Sprintf("%d", port.Port)
			} else {
				ports = fmt.Sprintf("%s,%d", ports, port.Port)
			}

			// Kubernetes creates records for all named ports, of the form
			// _my-port-name._my-port-protocol.my-svc.my-namespace.svc.cluster-domain.example
			// https://kubernetes.io/docs/concepts/services-networking/dns-pod-service/#srv-records
			if port.Name != "" {
				proto := strings.ToLower(string(port.Protocol))
				table.Routes = append(table.Routes, &daemon.Route{
					Name:   fmt.Sprintf("_%v._%v.%v", port.Name, proto, qName),
					Ip:     ip,
					Port:   ports,
					Proto:  proto,
					Target: ProxyRedirPort,
				})
			}
		}

		table.Routes = append(table.Routes, &daemon.Route{
			Name:   qName,
			Ip:     ip,
			Port:   ports,
			Proto:  "tcp",
			Target: ProxyRedirPort,
		})
	}
	for _, pod := range kc.Pods {
		qname := ""

		hostname := pod.Spec.Hostname
		if hostname != "" {
			qname += hostname
		}

		subdomain := pod.Spec.Subdomain
		if subdomain != "" {
			qname += "." + subdomain
		}

		if qname == "" {
			// Note: this is a departure from kubernetes, kubernetes will
			// simply not publish a dns name in this case.
			qname = pod.Name + "." + pod.Namespace + ".pod.cluster.local"
		} else {
			qname += ".svc.cluster.local"
		}

		ip := pod.Status.PodIP
		if ip != "" {
			table.Routes = append(table.Routes, &daemon.Route{
				Name:   qname,
				Ip:     ip,
				Proto:  "tcp",
				Target: ProxyRedirPort,
			})
		}
	}

	// Send updated table to daemon
	dlog.Debugf(c, "sending table update for table iptables %s", table.Name)
	if _, err := kc.daemon.Update(c, &table); err != nil {
		dlog.Errorf(c, "error posting update to %s: %v", table.Name, err)
	}
}

// deploymentNames  returns the names found in the current namespace of the last deployments
// snapshot produced by the accumulator
func (kc *k8sCluster) deploymentNames() []string {
	kc.accLock.Lock()
	depNames := make([]string, 0)
	for _, dep := range kc.Deployments {
		if dep.Namespace == kc.Namespace {
			depNames = append(depNames, dep.Name)
		}
	}
	kc.accLock.Unlock()
	return depNames
}

// findDeployment finds a deployment with the given name in the clusters namespace and returns
// either a copy of that deployment or nil if no such deployment could be found.
func (kc *k8sCluster) findDeployment(name string) *kates.Deployment {
	kc.accLock.Lock()
	defer kc.accLock.Unlock()
	return kc.findDeploymentUnlocked(name)
}

// findDeploymentUnlocked is like findDeployment, but assumes that kc.accLock is already held.
func (kc *k8sCluster) findDeploymentUnlocked(name string) *kates.Deployment {
	for _, dep := range kc.Deployments {
		if dep.Namespace == kc.Namespace && dep.Name == name {
			return dep.DeepCopy()
		}
	}
	return nil
}

// findSvc finds a service with the given name in the clusters namespace and returns
// either a copy of that service or nil if no such service could be found.
func (kc *k8sCluster) findSvc(name string) *kates.Service {
	var svcCopy *kates.Service
	kc.accLock.Lock()
	for _, svc := range kc.Services {
		if svc.Namespace == kc.Namespace && svc.Name == name {
			svcCopy = svc.DeepCopy()
			break
		}
	}
	kc.accLock.Unlock()
	return svcCopy
}

// findAllSvc finds a service with the given service type in all namespaces of the clusters returns
// a slice containing a copy of those services.
func (kc *k8sCluster) findAllSvcByType(svcType v1.ServiceType) []*kates.Service {
	var svcCopies []*kates.Service
	kc.accLock.Lock()
	for _, svc := range kc.Services {
		if svc.Spec.Type == svcType {
			svcCopies = append(svcCopies, svc.DeepCopy())
			break
		}
	}
	kc.accLock.Unlock()
	return svcCopies
}

func newKCluster(c context.Context, kubeFlagMap map[string]string, daemon daemon.DaemonClient) (*k8sCluster, error) {
	kubeFlagArgs := make([]string, 0, len(kubeFlagMap))
	kubeFlagConfig := kates.NewConfigFlags(false)
	kubeFlagSet := pflag.NewFlagSet("", 0)
	kubeFlagConfig.AddFlags(kubeFlagSet)
	for k, v := range kubeFlagMap {
		kubeFlagArgs = append(kubeFlagArgs, "--"+k+"="+v)
		if err := kubeFlagSet.Set(k, v); err != nil {
			err = errors.Wrapf(err, "processing kubectl flag %q", "--"+k+"="+v)
			return nil, err
		}
	}

	// TODO: All shell-outs to kubectl here should go through the kates client.
	ctxName := kubeFlagMap["context"]
	if ctxName == "" {
		cmd := dexec.CommandContext(c, "kubectl", append(kubeFlagArgs, "config", "current-context")...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("kubectl config current-context: %v", client.RunError(err))
		}
		ctxName = strings.TrimSpace(string(output))
	}

	namespace := kubeFlagMap["namespace"]
	if namespace == "" {
		nsQuery := fmt.Sprintf("jsonpath={.contexts[?(@.name==\"%s\")].context.namespace}", ctxName)
		cmd := dexec.CommandContext(c, "kubectl", append(kubeFlagArgs, "config", "view", "-o", nsQuery)...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("kubectl config view ns failed: %v", client.RunError(err))
		}
		namespace = strings.TrimSpace(string(output))
		if namespace == "" { // This is what kubens does
			namespace = "default"
		}
	}

	kc, err := kates.NewClientFromConfigFlags(kubeFlagConfig)
	if err != nil {
		return nil, err
	}

	ret := &k8sCluster{
		Context:      ctxName,
		Namespace:    namespace,
		kubeFlagArgs: kubeFlagArgs,

		client: kc,
		daemon: daemon,
	}
	ret.accCond = sync.NewCond(&ret.accLock)

	if err := ret.check(c); err != nil {
		return nil, fmt.Errorf("initial cluster check failed: %v", client.RunError(err))
	}

	server := kubeFlagMap["server"]
	if server == "" {
		// By using --minify, we prune all clusters not relevant to the current context, so
		// we can just use "[0]" instead of correlating the cluster index with the context.
		//
		// In order to have a helpful error message when the context doesn't exist (so
		// .clusters==[]), we do this *after* the .check().
		srvQuery := "jsonpath={.clusters[0].cluster.server}"
		cmd := dexec.CommandContext(c, "kubectl", append(kubeFlagArgs, "config", "view", "--minify", "-o", srvQuery)...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("kubectl config view server: %v", client.RunError(err))
		}
		server = strings.TrimSpace(string(output))
	}
	ret.Server = server

	return ret, nil
}

// trackKCluster tracks connectivity to a cluster
func trackKCluster(c context.Context, kubeFlagMap map[string]string, daemon daemon.DaemonClient) (*k8sCluster, error) {
	kc, err := newKCluster(c, kubeFlagMap, daemon)
	if err != nil {
		return nil, fmt.Errorf("k8s client create failed: %v", err)
	}

	dlog.Infof(c, "Context: %s", kc.Context)
	dlog.Infof(c, "Server: %s", kc.Server)

	// accWait is closed when the watch produces its first snapshot
	accWait := make(chan struct{})
	if err := kc.startWatches(c, kates.NamespaceAll, accWait); err != nil {
		dlog.Errorf(c, "watch all namespaces: %+v", err)
		dlog.Errorf(c, "falling back to watching only %q", kc.Namespace)
		err = kc.startWatches(c, kc.Namespace, accWait)
		if err != nil {
			close(accWait)
			return nil, err
		}
	}
	// wait until accumulator has produced its first snapshot. A context cancel is guaranteed to also close this channel
	<-accWait

	return kc, nil
}

func (kc *k8sCluster) getClusterId(c context.Context) (clusterID string) {
	rootID := func() (rootID string) {
		defer func() {
			// If kates panics, we'll use the default rootID, so we
			// can recover here
			_ = recover()
		}()
		rootID = "00000000-0000-0000-0000-000000000000"

		nsName := "default"
		ns := &kates.Namespace{
			TypeMeta:   kates.TypeMeta{Kind: "Namespace"},
			ObjectMeta: kates.ObjectMeta{Name: nsName},
		}
		if err := kc.client.Get(c, ns, ns); err != nil {
			return
		}

		rootID = string(ns.GetUID())
		return
	}()
	return rootID
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
