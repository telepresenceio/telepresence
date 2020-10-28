package connector

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	route "github.com/datawire/telepresence2/pkg/rpc/iptables"

	"github.com/datawire/telepresence2/pkg/rpc/daemon"
	"github.com/pkg/errors"

	"github.com/datawire/ambassador/pkg/k8s"
	"github.com/datawire/ambassador/pkg/supervisor"
)

// worker names
const (
	BridgeWorker         = "BRG"
	K8sBridgeWorker      = "K8S"
	K8sPortForwardWorker = "KPF"
	K8sSSHWorker         = "SSH"
	K8sApplyWorker       = "KAP"
	DkrBridgeWorker      = "DKR"
)

// ProxyRedirPort is the port to which we redirect proxied IPs. It
// should probably eventually be configurable and/or dynamically
// chosen.
const ProxyRedirPort = "1234"

const podManifest = `
---
apiVersion: v1
kind: Pod
metadata:
  name: teleproxy
  labels:
    name: teleproxy
spec:
  hostname: traffic-proxy
  containers:
  - name: proxy
    image: docker.io/datawire/telepresence-k8s:0.75
    ports:
    - protocol: TCP
      containerPort: 8022
`

var (
	errAborted = errors.New("aborted")
)

// teleproxy holds the configuration for a Teleproxy
type bridge struct {
	kubeConfig string
	context    string
	namespace  string
	daemon     daemon.DaemonClient
	workers    []*supervisor.Worker
}

type svcResource struct {
	Spec svcSpec
}

type svcSpec struct {
	ClusterIP string
	Ports     []svcPort
}

type svcPort struct {
	Name     string
	Port     int
	Protocol string
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

func newBridge(kubeConfig, context, namespace string, daemon daemon.DaemonClient) *bridge {
	return &bridge{
		kubeConfig: kubeConfig,
		context:    context,
		namespace:  namespace,
		daemon:     daemon,
	}
}

func (t *bridge) addWorker(p *supervisor.Process, worker *supervisor.Worker) {
	p.Supervisor().Supervise(worker)
	t.workers = append(t.workers, worker)
}

func (t *bridge) connect(p *supervisor.Process) {
	t.addWorker(p, &supervisor.Worker{
		Name: K8sApplyWorker,
		Work: func(p *supervisor.Process) (err error) {
			kubeInfo := k8s.NewKubeInfo(t.kubeConfig, t.context, t.namespace)
			// setup remote teleproxy pod
			args, err := kubeInfo.GetKubectlArray("apply", "-f", "-")
			if err != nil {
				return err
			}
			apply := p.Command("kubectl", args...)
			apply.Stdin = strings.NewReader(podManifest)
			err = apply.Start()
			if err != nil {
				return
			}
			err = p.DoClean(apply.Wait, apply.Process.Kill)
			if err != nil {
				return
			}
			p.Ready()
			// we need to stay alive so that our dependencies can start
			<-p.Shutdown()
			return
		},
	})

	t.addWorker(p, &supervisor.Worker{
		Name:     K8sPortForwardWorker,
		Requires: []string{K8sApplyWorker},
		Retry:    true,
		Work: func(p *supervisor.Process) (err error) {
			kubeInfo := k8s.NewKubeInfo(t.kubeConfig, t.context, t.namespace)
			args, err := kubeInfo.GetKubectlArray("port-forward", "pod/teleproxy", "8022")
			if err != nil {
				return err
			}
			pf := p.Command("kubectl", args...)
			err = pf.Start()
			if err != nil {
				return
			}
			p.Ready()
			err = p.DoClean(func() error {
				err := pf.Wait()
				if err != nil {
					args, err := kubeInfo.GetKubectlArray("get", "pod/teleproxy")
					if err != nil {
						return err
					}
					inspect := p.Command("kubectl", args...)
					_ = inspect.Run() // Discard error as this is just for logging
				}
				return err
			}, func() error {
				return pf.Process.Kill()
			})
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

func (t *bridge) updateTable(p *supervisor.Process, w *k8s.Watcher) {
	table := route.Table{Name: "kubernetes"}

	for _, svc := range w.List("services") {
		decoded := svcResource{}
		err := svc.Decode(&decoded)
		if err != nil {
			p.Logf("error decoding service: %v", err)
			continue
		}

		spec := decoded.Spec

		ip := spec.ClusterIP
		// for headless services the IP is None, we
		// should properly handle these by listening
		// for endpoints and returning multiple A
		// records at some point
		if ip != "" && ip != "None" {
			qName := svc.Name() + "." + svc.Namespace() + ".svc.cluster.local"

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
					table.Routes = append(table.Routes, &route.Route{
						Name:   fmt.Sprintf("_%v._%v.%v", port.Name, strings.ToLower(port.Protocol), qName),
						Ip:     ip,
						Port:   ports,
						Proto:  strings.ToLower(port.Protocol),
						Target: ProxyRedirPort,
					})
				}
			}

			table.Routes = append(table.Routes, &route.Route{
				Name:   qName,
				Ip:     ip,
				Port:   ports,
				Proto:  "tcp",
				Target: ProxyRedirPort,
			})
		}
	}

	for _, pod := range w.List("pods") {
		qname := ""

		hostname, ok := pod.Spec()["hostname"]
		if ok && hostname != "" {
			qname += hostname.(string)
		}

		subdomain, ok := pod.Spec()["subdomain"]
		if ok && subdomain != "" {
			qname += "." + subdomain.(string)
		}

		if qname == "" {
			// Note: this is a departure from kubernetes, kubernetes will
			// simply not publish a dns name in this case.
			qname = pod.Name() + "." + pod.Namespace() + ".pod.cluster.local"
		} else {
			qname += ".svc.cluster.local"
		}

		ip, ok := pod.Status()["podIP"]
		if ok && ip != "" {
			table.Routes = append(table.Routes, &route.Route{
				Name:   qname,
				Ip:     ip.(string),
				Proto:  "tcp",
				Target: ProxyRedirPort,
			})
		}
	}

	// Send updated table to daemon
	if _, err := t.daemon.Update(p.Context(), &table); err != nil {
		log.Printf("BRG: error posting update to %s: %v", table.Name, err)
	}
}

func (t *bridge) startWatches(p *supervisor.Process, kubeInfo *k8s.KubeInfo, namespace string) (w *k8s.Watcher, err error) {
	w, err = k8s.NewWatcher(kubeInfo)
	if err != nil {
		return nil, err
	}

	if err = w.WatchQuery(
		k8s.Query{Kind: "services", Namespace: namespace},
		func(w *k8s.Watcher) { t.updateTable(p, w) },
	); err != nil {
		// FIXME why do we ignore this error?
		p.Logf("watch services: %+v", err)
	}

	if err = w.WatchQuery(
		k8s.Query{Kind: "pods", Namespace: namespace},
		func(w *k8s.Watcher) { t.updateTable(p, w) },
	); err != nil {
		// FIXME why do we ignore this error?
		p.Logf("watch pods: %+v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			w = nil
			if pErr, ok := r.(error); ok {
				err = pErr
			} else {
				err = errors.Errorf("w.Start(): %+v", r)
			}
		}
	}()

	w.Start() // may panic

	return w, nil
}

func (t *bridge) bridgeWorker(p *supervisor.Process) error {
	// setup kubernetes bridge
	kubeInfo := k8s.NewKubeInfo(t.kubeConfig, t.context, t.namespace)

	// Set up DNS search path based on current Kubernetes namespace
	namespace, err := kubeInfo.Namespace()
	if err != nil {
		return err
	}
	p.Logf("kubernetes namespace=%s", namespace)
	paths := []string{
		namespace + ".svc.cluster.local.",
		"svc.cluster.local.",
		"cluster.local.",
		"",
	}
	log.Println("BRG: Setting DNS search path:", paths[0])
	_, err = t.daemon.SetDnsSearchPath(p.Context(), &daemon.Paths{Paths: paths})
	if err != nil {
		log.Printf("BRG: error setting up search path: %v", err)
		panic(err) // Because this will fail if we win the startup race
	}

	var w *k8s.Watcher

	// start watcher using DoClean so that the user can interrupt it --
	// it can take a while depending on the cluster and on connectivity.
	err = p.DoClean(func() error {
		var err error
		if w, err = t.startWatches(p, kubeInfo, k8s.NamespaceAll); err == nil {
			return nil
		}

		p.Logf("watch all namespaces: %+v", err)

		ns, err := kubeInfo.Namespace()
		if err != nil {
			return err
		}

		p.Logf("falling back to watching only %q", ns)
		w, err = t.startWatches(p, kubeInfo, ns)

		return err
	}, func() error {
		return errAborted
	})

	if err == errAborted {
		return nil
	}

	if err != nil {
		return err
	}

	p.Ready()
	<-p.Shutdown()
	w.Stop()

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
	address := "traffic-proxy.svc:8022"
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
