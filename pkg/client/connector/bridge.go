package connector

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/datawire/telepresence2/pkg/rpc/daemon"
	"github.com/datawire/telepresence2/pkg/rpc/iptables"
)

// worker names
const (
	K8sBridgeWorker      = "K8S"
	K8sPortForwardWorker = "KPF"
	K8sSSHWorker         = "SSH"
)

// ProxyRedirPort is the port to which we redirect proxied IPs. It
// should probably eventually be configurable and/or dynamically
// chosen.
const ProxyRedirPort = "1234"

// teleproxy holds the configuration for a Teleproxy
type bridge struct {
	*k8sCluster
	sshPort int32
	daemon  daemon.DaemonClient
	workers []*supervisor.Worker
}

func (t *bridge) restart() {
	for _, w := range t.workers {
		w.Shutdown()
	}
	for _, w := range t.workers {
		w.Wait()
	}
	for _, w := range t.workers {
		w.Restart()
	}
}

func newBridge(kc *k8sCluster, daemon daemon.DaemonClient, sshPort int32) *bridge {
	return &bridge{
		k8sCluster: kc,
		daemon:     daemon,
		sshPort:    sshPort,
	}
}

func (t *bridge) addWorker(p *supervisor.Process, worker *supervisor.Worker) {
	p.Supervisor().Supervise(worker)
	t.workers = append(t.workers, worker)
}

func (t *bridge) connect(p *supervisor.Process) {
	t.addWorker(p, &supervisor.Worker{
		Name: K8sPortForwardWorker,
		// Requires: []string{K8sApplyWorker},
		Retry: true,
		Work: func(p *supervisor.Process) (err error) {
			// t.kubeConfig, t.context, t.namespace)
			var pods []*kates.Pod
			err = t.client.List(p.Context(), kates.Query{
				Kind:          "pod",
				Namespace:     t.Namespace,
				LabelSelector: "app=traffic-manager",
			}, &pods)
			if err != nil {
				return err
			}
			if len(pods) == 0 {
				return fmt.Errorf("found no pod with label app=traffic-manager in namespace %s", t.Namespace)
			}
			podName := strings.TrimSpace(pods[0].Name)

			pf := p.Command("kubectl", append(t.getKubectlArgs(), "port-forward", fmt.Sprintf("pod/%s", podName), "8022")...)
			err = pf.Start()
			if err != nil {
				return
			}
			p.Ready()
			err = p.DoClean(pf.Wait, pf.Process.Kill)
			return
		},
	})

	t.addWorker(p, &supervisor.Worker{
		Name:     K8sSSHWorker,
		Requires: []string{K8sPortForwardWorker},
		Retry:    true,
		Work: func(p *supervisor.Process) (err error) {
			// XXX: probably need some kind of keepalive check for ssh, first
			// curl after wakeup seems to trigger detection of death
			ssh := p.Command("ssh", "-D", "localhost:1080", "-C", "-N", "-oConnectTimeout=5",
				"-oExitOnForwardFailure=yes", "-oStrictHostKeyChecking=no",
				"-oUserKnownHostsFile=/dev/null", "telepresence@localhost", "-p", "8022")
			err = ssh.Start()
			if err != nil {
				return
			}
			p.Ready()
			return p.DoClean(ssh.Wait, ssh.Process.Kill)
		},
	})
}

type bridgeData struct {
	Pods     []*kates.Pod
	Services []*kates.Service
}

func (t *bridge) updateTable(p *supervisor.Process, snapshot *bridgeData) {
	table := iptables.Table{Name: "kubernetes"}
	for _, svc := range snapshot.Services {
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
				table.Routes = append(table.Routes, &iptables.Route{
					Name:   fmt.Sprintf("_%v._%v.%v", port.Name, proto, qName),
					Ip:     ip,
					Port:   ports,
					Proto:  proto,
					Target: ProxyRedirPort,
				})
			}
		}

		table.Routes = append(table.Routes, &iptables.Route{
			Name:   qName,
			Ip:     ip,
			Port:   ports,
			Proto:  "tcp",
			Target: ProxyRedirPort,
		})
	}
	for _, pod := range snapshot.Pods {
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
			table.Routes = append(table.Routes, &iptables.Route{
				Name:   qname,
				Ip:     ip,
				Proto:  "tcp",
				Target: ProxyRedirPort,
			})
		}
	}

	// Send updated table to daemon
	if _, err := t.daemon.Update(p.Context(), &table); err != nil {
		p.Logf("error posting update to %s: %v", table.Name, err)
	}
}

func (t *bridge) createWatch(p *supervisor.Process, namespace string) (acc *kates.Accumulator, err error) {
	defer func() {
		if r := recover(); r != nil {
			switch r := r.(type) {
			case error:
				err = r
			case string:
				err = errors.New(r)
			default:
				panic(r)
			}
		}
	}()

	return t.client.Watch(p.Context(),
		kates.Query{
			Name:      "Services",
			Namespace: namespace,
			Kind:      "service",
		},
		kates.Query{
			Name:      "Pods",
			Namespace: namespace,
			Kind:      "pod",
		}), nil
}

func (t *bridge) startWatches(p *supervisor.Process, namespace string) error {
	acc, err := t.createWatch(p, namespace)
	if err != nil {
		return err
	}
	snapshot := bridgeData{}

	go func() {
		for {
			select {
			case <-p.Shutdown():
				return
			case <-acc.Changed():
				if acc.Update(&snapshot) {
					t.updateTable(p, &snapshot)
				}
			}
		}
	}()
	return nil
}

func (t *bridge) bridgeWorker(p *supervisor.Process) error {
	// setup kubernetes bridge
	p.Logf("kubernetes namespace=%s", t.Namespace)
	paths := []string{
		t.Namespace + ".svc.cluster.local.",
		"svc.cluster.local.",
		"cluster.local.",
		"",
	}
	p.Logf("Setting DNS search path: %s", paths[0])
	_, err := t.daemon.SetDnsSearchPath(p.Context(), &daemon.Paths{Paths: paths})
	if err != nil {
		p.Logf("error setting up search path: %v", err)
		panic(err) // Because this will fail if we win the startup race
	}

	// start watcher using DoClean so that the user can interrupt it --
	// it can take a while depending on the cluster and on connectivity.
	if err = t.startWatches(p, metav1.NamespaceAll); err == nil {
		return nil
	}
	p.Logf("watch all namespaces: %+v", err)

	p.Logf("falling back to watching only %q", t.Namespace)
	err = t.startWatches(p, t.Namespace)

	if err != nil {
		return err
	}

	p.Ready()
	<-p.Shutdown()
	return nil
}

func (t *bridge) start(p *supervisor.Process) error {
	if err := checkKubectl(p); err != nil {
		return err
	}
	t.connect(p)
	t.addWorker(p, &supervisor.Worker{
		Name: K8sBridgeWorker,
		Work: t.bridgeWorker})
	return nil
}

const kubectlErr = "kubectl version 1.10 or greater is required"

func checkKubectl(p *supervisor.Process) error {
	output, err := p.Command("kubectl", "version", "--client", "-o", "json").Capture(nil)
	if err != nil {
		return errors.Wrap(err, kubectlErr)
	}

	var info struct {
		ClientVersion struct {
			Major string
			Minor string
		}
	}

	if err = json.Unmarshal([]byte(output), &info); err != nil {
		return errors.Wrap(err, kubectlErr)
	}

	major, err := strconv.Atoi(info.ClientVersion.Major)
	if err != nil {
		return errors.Wrap(err, kubectlErr)
	}
	minor, err := strconv.Atoi(info.ClientVersion.Minor)
	if err != nil {
		return errors.Wrap(err, kubectlErr)
	}

	if major != 1 || minor < 10 {
		return errors.Errorf("%s (found %d.%d)", kubectlErr, major, minor)
	}
	return nil
}

// check checks the status of teleproxy bridge by doing the equivalent of
//  curl http://traffic-proxy.svc:8022.
// Note there is no namespace specified, as we are checking for bridge status in the
// current namespace.
func (t *bridge) check(p *supervisor.Process) bool {
	address := fmt.Sprintf("localhost:%d", t.sshPort)
	conn, err := net.DialTimeout("tcp", address, 15*time.Second)
	if err != nil {
		p.Logf("fail to establish tcp connection to %s: %s", address, err.Error())
		return false
	}
	defer conn.Close()

	msg, _, err := bufio.NewReader(conn).ReadLine()
	if err != nil {
		p.Logf("tcp read: %s", err.Error())
		return false
	}
	if !strings.Contains(string(msg), "SSH") {
		p.Logf("expected SSH prompt, got: %v", string(msg))
		return false
	}
	return true
}
