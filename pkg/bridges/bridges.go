package bridges

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/datawire/ambassador/pkg/k8s"
	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/pkg/errors"

	"github.com/datawire/telepresence2/pkg/interceptor"
	"github.com/datawire/telepresence2/pkg/route"
)

var (
	errAborted = errors.New("aborted")
)

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

func updateTable(p *supervisor.Process, w *k8s.Watcher) {
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
					table.Add(route.Route{
						Name:   fmt.Sprintf("_%v._%v.%v", port.Name, strings.ToLower(port.Protocol), qName),
						Ip:     ip,
						Port:   ports,
						Proto:  strings.ToLower(port.Protocol),
						Target: interceptor.ProxyRedirPort,
					})
				}
			}

			table.Add(route.Route{
				Name:   qName,
				Ip:     ip,
				Port:   ports,
				Proto:  "tcp",
				Target: interceptor.ProxyRedirPort,
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
			table.Add(route.Route{
				Name:   qname,
				Ip:     ip.(string),
				Proto:  "tcp",
				Target: interceptor.ProxyRedirPort,
			})
		}
	}

	post(table)
}

func startWatches(p *supervisor.Process, kubeInfo *k8s.KubeInfo, namespace string) (w *k8s.Watcher, err error) {
	w, err = k8s.NewWatcher(kubeInfo)
	if err != nil {
		return nil, err
	}

	if err := w.WatchQuery(
		k8s.Query{Kind: "services", Namespace: namespace},
		func(w *k8s.Watcher) { updateTable(p, w) },
	); err != nil {
		// FIXME why do we ignore this error?
		p.Logf("watch services: %+v", err)
	}

	if err := w.WatchQuery(
		k8s.Query{Kind: "pods", Namespace: namespace},
		func(w *k8s.Watcher) { updateTable(p, w) },
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

func (t *config) bridges(p *supervisor.Process) {
	t.connect(p)

	t.addWorker(p, &supervisor.Worker{
		Name: K8sBridgeWorker,
		Work: func(p *supervisor.Process) error {
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
			// log.Println("BRG: Setting DNS search path:", paths[0])
			body, err := json.Marshal(paths)
			if err != nil {
				panic(err)
			}
			ign, err := http.Post("http://teleproxy/api/search", "application/json", bytes.NewReader(body))
			if err != nil {
				log.Printf("BRG: error setting up search path: %v", err)
				panic(err) // Because this will fail if we win the startup race
			}
			defer ign.Body.Close()

			var w *k8s.Watcher

			// Start watcher using DoClean so that the user can interrupt it --
			// it can take a while depending on the cluster and on connectivity.
			err = p.DoClean(func() error {
				var err error
				w, err = startWatches(p, kubeInfo, k8s.NamespaceAll)
				if err == nil {
					return nil
				}

				// p.Logf("watch all namespaces: %+v", err)

				ns, err := kubeInfo.Namespace()
				if err != nil {
					return err
				}

				// p.Logf("falling back to watching only %q", ns)
				w, err = startWatches(p, kubeInfo, ns)

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
		},
	})
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

	err = json.Unmarshal([]byte(output), &info)
	if err != nil {
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

// CheckBridge checks the status of teleproxy bridge by doing the equivalent of
//  curl http://traffic-proxy.svc:8022.
// Note there is no namespace specified, as we are checking for bridge status in the
// current namespace.
func (t *config) Check(p *supervisor.Process) bool {
	address := "traffic-proxy.svc:8022"
	conn, err := net.DialTimeout("tcp", address, 15*time.Second)
	if err != nil {
		p.Logf("fail to establish tcp connection to %v: %s", address, err.Error())
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

func (t *config) Start(p *supervisor.Process) error {
	err := checkKubectl(p)
	if err != nil {
		return err
	}
	t.bridges(p)
	return nil
}

func post(tables ...route.Table) {
	names := make([]string, len(tables))
	for i, t := range tables {
		names[i] = t.Name
	}
	jNames := strings.Join(names, ", ")

	body, err := json.Marshal(tables)
	if err != nil {
		panic(err)
	}
	resp, err := http.Post("http://teleproxy/api/tables/", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("BRG: error posting update to %s: %v", jNames, err)
	} else {
		// log.Printf("BRG: posted update to %s: %v", jNames, resp.StatusCode)
		resp.Body.Close()
	}
}
