package connector

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dutil"
	"github.com/pkg/errors"

	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/rpc/daemon"
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
	cancel  context.CancelFunc
}

func newBridge(kc *k8sCluster, daemon daemon.DaemonClient, sshPort int32) *bridge {
	return &bridge{
		k8sCluster: kc,
		daemon:     daemon,
		sshPort:    sshPort,
	}
}

/*
func (t *bridge) restart(c context.Context) error {
	if cancel := t.cancel; cancel != nil {
		t.cancel = nil
		cancel()
	}
	return t.start(c)
}
*/

type bridgeData struct {
	Pods     []*kates.Pod
	Services []*kates.Service
}

func (br *bridge) updateTable(c context.Context, snapshot *bridgeData) {
	table := daemon.Table{Name: "kubernetes"}
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
	if _, err := br.daemon.Update(c, &table); err != nil {
		dlog.Errorf(c, "error posting update to %s: %v", table.Name, err)
	}
}

func (br *bridge) createWatch(c context.Context, namespace string) (acc *kates.Accumulator, err error) {
	defer func() {
		if r := dutil.PanicToError(recover()); r != nil {
			err = r
		}
	}()

	return br.client.Watch(c,
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

func (br *bridge) start(c context.Context) error {
	if err := checkKubectl(c); err != nil {
		return err
	}
	c, br.cancel = context.WithCancel(c)

	g := dgroup.ParentGroup(c)
	g.Go(K8sPortForwardWorker, func(c context.Context) error {
		return client.Retry(c, br.portForwardWorker, 2*time.Second, 15*time.Second, time.Minute)
	})
	g.Go(K8sBridgeWorker, br.bridgeWorker)
	return nil
}

func (br *bridge) bridgeWorker(c context.Context) error {
	// setup kubernetes bridge
	dlog.Infof(c, "kubernetes namespace=%s", br.Namespace)
	paths := []string{
		br.Namespace + ".svc.cluster.local.",
		"svc.cluster.local.",
		"cluster.local.",
		"",
	}
	dlog.Infof(c, "Setting DNS search path: %s", paths[0])
	_, err := br.daemon.SetDnsSearchPath(c, &daemon.Paths{Paths: paths})
	if err != nil {
		dlog.Errorf(c, "error setting up search path: %v", err)
		panic(err) // Because this will fail if we win the startup race
	}

	// start watcher using DoClean so that the user can interrupt it --
	// it can take a while depending on the cluster and on connectivity.
	if err = br.startWatches(c, kates.NamespaceAll); err == nil {
		return nil
	}
	dlog.Errorf(c, "watch all namespaces: %+v", err)
	dlog.Errorf(c, "falling back to watching only %q", br.Namespace)
	return br.startWatches(c, br.Namespace)
}

func (br *bridge) portForwardWorker(c context.Context) error {
	var pods []*kates.Pod
	err := br.client.List(c, kates.Query{
		Kind:          "pod",
		Namespace:     br.Namespace,
		LabelSelector: "app=traffic-manager",
	}, &pods)
	if err != nil {
		return err
	}
	if len(pods) == 0 {
		return fmt.Errorf("found no pod with label app=traffic-manager in namespace %s", br.Namespace)
	}
	podName := strings.TrimSpace(pods[0].Name)
	kpfArgs := []string{"port-forward", fmt.Sprintf("pod/%s", podName), "8022"}
	return br.portForwardAndThen(c, kpfArgs, K8sSSHWorker, br.sshWorker)
}

func (br *bridge) startWatches(c context.Context, namespace string) error {
	acc, err := br.createWatch(c, namespace)
	if err != nil {
		return err
	}
	snapshot := bridgeData{}

	dgroup.ParentGroup(c).Go("watch-k8s", func(c context.Context) error {
		for {
			select {
			case <-c.Done():
				return nil
			case <-acc.Changed():
				if acc.Update(&snapshot) {
					br.updateTable(c, &snapshot)
				}
			}
		}
	})
	return nil
}

func (br *bridge) sshWorker(c context.Context) error {
	// XXX: probably need some kind of keepalive check for ssh, first
	// curl after wakeup seems to trigger detection of death
	ssh := dexec.CommandContext(c, "ssh", "-D", "localhost:1080", "-C", "-N", "-oConnectTimeout=5",
		"-oExitOnForwardFailure=yes", "-oStrictHostKeyChecking=no",
		"-oUserKnownHostsFile=/dev/null", "telepresence@localhost", "-p", "8022")
	return ssh.Run()
}

const kubectlErr = "kubectl version 1.10 or greater is required"

func checkKubectl(c context.Context) error {
	output, err := dexec.CommandContext(c, "kubectl", "version", "--client", "-o", "json").Output()
	if err != nil {
		return errors.Wrap(err, kubectlErr)
	}

	var info struct {
		ClientVersion struct {
			Major string
			Minor string
		}
	}

	if err = json.Unmarshal(output, &info); err != nil {
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
//  curl http://traffic-manager.svc:8022.
// Note there is no namespace specified, as we are checking for bridge status in the
// current namespace.
func (br *bridge) check(c context.Context) bool {
	address := fmt.Sprintf("localhost:%d", br.sshPort)
	conn, err := net.DialTimeout("tcp", address, 15*time.Second)
	if err != nil {
		dlog.Errorf(c, "fail to establish tcp connection to %s: %s", address, err.Error())
		return false
	}
	defer conn.Close()

	msg, _, err := bufio.NewReader(conn).ReadLine()
	if err != nil {
		dlog.Errorf(c, "tcp read: %s", err.Error())
		return false
	}
	if !strings.Contains(string(msg), "SSH") {
		dlog.Errorf(c, "expected SSH prompt, got: %v", string(msg))
		return false
	}
	return true
}
