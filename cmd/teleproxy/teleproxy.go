package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"git.lukeshu.com/go/libsystemd/sd_daemon"
	"github.com/pkg/errors"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"
	"github.com/datawire/teleproxy/pkg/tpu"

	"github.com/datawire/teleproxy/internal/pkg/api"
	"github.com/datawire/teleproxy/internal/pkg/dns"
	"github.com/datawire/teleproxy/internal/pkg/docker"
	"github.com/datawire/teleproxy/internal/pkg/interceptor"
	"github.com/datawire/teleproxy/internal/pkg/proxy"
	"github.com/datawire/teleproxy/internal/pkg/route"
)

func dnsListeners(port string) (listeners []string) {
	// turns out you need to listen on localhost for nat to work
	// properly for udp, otherwise you get an "unexpected source
	// blah thingy" because the dns reply packets look like they
	// are coming from the wrong place
	listeners = append(listeners, "127.0.0.1:"+port)

	if runtime.GOOS == "linux" {
		// This is the default docker bridge. We should
		// probably figure out how to query this out of docker
		// instead of hardcoding it. We need to listen here
		// because the nat logic we use to intercept dns
		// packets will divert the packet to the interface it
		// originates from, which in the case of containers is
		// the docker bridge. Without this dns won't work from
		// inside containers.
		listeners = append(listeners, "172.17.0.1:"+port)
	}

	return
}

var Version = "(unknown version)"

const (
	DEFAULT   = ""
	INTERCEPT = "intercept"
	BRIDGE    = "bridge"
	VERSION   = "version"

	// This is the port to which we redirect dns requests. It should probably eventually be configurable and/or
	// dynamically chosen
	DNS_REDIR_PORT = "1233"

	// This is the port to which we redirect proxied ips. It should probably eventually be configurable and/or
	// dynamically chosen.
	PROXY_REDIR_PORT = "1234"

	// This is a magic ip from the localhost range that we resolve "teleproxy" to and intercept for convenient
	// access to the teleproxy api server. This enables things like `curl teleproxy/api/tables/`. In theory this
	// could be any arbitrary value that is unlikely to conflict with a real world ip, but it is also handy for it
	// to be fixed so that we can debug even if dns isn't working by doing stuff like `curl
	// 127.254.254.254/api/...`. This value happens to be the last value in the ipv4 localhost range.
	MAGIC_IP = "127.254.254.254"
)

func main() {
	var version = flag.Bool("version", false, "alias for '-mode=version'")
	var mode = flag.String("mode", "", "mode of operation ('intercept', 'bridge', or 'version')")
	var kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	var kcontext = flag.String("context", "", "context to use (default: the current context)")
	var namespace = flag.String("namespace", "", "namespace to use (default: the current namespace for the context")
	var dnsIP = flag.String("dns", "", "dns ip address")
	var fallbackIP = flag.String("fallback", "", "dns fallback")

	flag.Parse()

	if *version {
		*mode = VERSION
	}

	switch *mode {
	case DEFAULT, INTERCEPT, BRIDGE:
		// do nothing
	case VERSION:
		fmt.Println("teleproxy", "version", Version)
		os.Exit(0)
	default:
		log.Fatalf("TPY: unrecognized mode: %v", *mode)
	}

	checkKubectl()

	// do this up front so we don't miss out on cleanup if someone
	// Control-C's just after starting us
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())
	sup := supervisor.WithContext(ctx)

	if *mode == DEFAULT || *mode == INTERCEPT {
		sup.Supervise(&supervisor.Worker{
			Name: "setup-intercept",
			Work: func(p *supervisor.Process) error {
				return intercept(p, *dnsIP, *fallbackIP)
			},
		})
	}

	if *mode == DEFAULT || *mode == BRIDGE {
		requires := []string{}
		if *mode != BRIDGE {
			requires = append(requires, "interceptor")
		}
		sup.Supervise(&supervisor.Worker{
			Name:     "setup-bridges",
			Requires: requires,
			Work: func(p *supervisor.Process) error {
				kubeinfo, err := k8s.NewKubeInfo(*kubeconfig, *kcontext, *namespace)
				if err != nil {
					return errors.Wrap(err, "k8s.NewKubeInfo")
				}
				bridges(p, kubeinfo)
				return nil
			},
		})
	}

	sup.Supervise(&supervisor.Worker{
		Name:     "ready-notifier",
		Requires: []string{"interceptor", "api-server", "dns-server", "proxy-server", "search-override"},
		Work: func(p *supervisor.Process) error {
			p.Do(func() {
				sd_daemon.Notification{State: "READY=1"}.Send(false)
				p.Ready()
			})
			<-p.Shutdown()
			return nil
		},
	})

	sup.Supervise(&supervisor.Worker{
		Name: "signal-handler",
		Work: func(p *supervisor.Process) error {
			select {
			case <-p.Shutdown():
			case s := <-signalChan:
				p.Logf("TPY: %v", s)
				cancel()
			}
			return nil
		},
	})

	sup.Run()
	log.Println("done")
}

func kubeDie(err error) {
	if err != nil {
		log.Println(err)
	}
	log.Fatal("kubectl version 1.10 or greater is required")
}

func checkKubectl() {
	output, err := tpu.Cmd("kubectl", "version", "--client", "-o", "json")
	if err != nil {
		kubeDie(err)
	}

	var info struct {
		ClientVersion struct {
			Major string
			Minor string
		}
	}

	err = json.Unmarshal([]byte(output), &info)
	if err != nil {
		kubeDie(err)
	}

	major, err := strconv.Atoi(info.ClientVersion.Major)
	if err != nil {
		kubeDie(err)
	}
	minor, err := strconv.Atoi(info.ClientVersion.Minor)
	if err != nil {
		kubeDie(err)
	}

	if major != 1 || minor < 10 {
		kubeDie(err)
	}
}

// intercept starts the interceptor, and only returns once the
// interceptor is successfully running in another goroutine.  It
// returns a function to call to shut down that goroutine.
//
// If dnsIP is empty, it will be detected from /etc/resolv.conf
//
// If fallbackIP is empty, it will default to Google DNS.
func intercept(p *supervisor.Process, dnsIP string, fallbackIP string) error {
	// xxx check that we are root

	if os.Geteuid() != 0 {
		return errors.New("ERROR: teleproxy must be run as root or suid root")
	}

	sup := p.Supervisor()

	if dnsIP == "" {
		dat, err := ioutil.ReadFile("/etc/resolv.conf")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(dat), "\n") {
			if strings.Contains(line, "nameserver") {
				fields := strings.Fields(line)
				dnsIP = fields[1]
				log.Printf("TPY: Automatically set -dns=%v", dnsIP)
				break
			}
		}
	}
	if dnsIP == "" {
		return errors.New("couldn't determine dns ip from /etc/resolv.conf")
	}

	if fallbackIP == "" {
		if dnsIP == "8.8.8.8" {
			fallbackIP = "8.8.4.4"
		} else {
			fallbackIP = "8.8.8.8"
		}
		log.Printf("TPY: Automatically set -fallback=%v", fallbackIP)
	}
	if fallbackIP == dnsIP {
		return errors.New("if your fallbackIP and your dnsIP are the same, you will have a dns loop")
	}

	iceptor := interceptor.NewInterceptor("teleproxy")
	apis, err := api.NewAPIServer(iceptor)
	if err != nil {
		return errors.Wrap(err, "API Server")
	}

	sup.Supervise(&supervisor.Worker{
		Name:     "interceptor",
		Requires: []string{}, // XXX: this will need to include the api server once it is changed to not bind early
		Work: func(p *supervisor.Process) error {
			iceptor.Start()
			bootstrap := route.Table{Name: "bootstrap"}
			bootstrap.Add(route.Route{
				Ip:     dnsIP,
				Target: DNS_REDIR_PORT,
				Proto:  "udp",
			})
			bootstrap.Add(route.Route{
				Name:   "teleproxy",
				Ip:     MAGIC_IP,
				Target: apis.Port(),
				Proto:  "tcp",
			})

			iceptor.Update(bootstrap)
			p.Ready()
			<-p.Shutdown()
			iceptor.Stop()
			return nil
		},
	})

	sup.Supervise(&supervisor.Worker{
		Name:     "api-server",
		Requires: []string{},
		Work: func(p *supervisor.Process) error {
			apis.Start()
			p.Ready()
			<-p.Shutdown()
			apis.Stop()
			return nil
		},
	})

	sup.Supervise(&supervisor.Worker{
		Name:     "dns-server",
		Requires: []string{},
		Work: func(p *supervisor.Process) error {
			srv := dns.Server{
				Listeners: dnsListeners(DNS_REDIR_PORT),
				Fallback:  fallbackIP + ":53",
				Resolve: func(domain string) string {
					route := iceptor.Resolve(domain)
					if route != nil {
						return route.Ip
					} else {
						return ""
					}
				},
			}
			err := srv.Start(p)
			if err != nil {
				return err
			}
			p.Ready()
			<-p.Shutdown()
			// there is no srv.Stop()
			return nil
		},
	})

	sup.Supervise(&supervisor.Worker{
		Name:     "proxy-server",
		Requires: []string{},
		Work: func(p *supervisor.Process) error {
			// hmm, we may not actually need to get the original
			// destination, we could just forward each ip to a unique port
			// and either listen on that port or run port-forward
			proxy, err := proxy.NewProxy(fmt.Sprintf(":%s", PROXY_REDIR_PORT), iceptor.Destination)
			if err != nil {
				return errors.Wrap(err, "Proxy")
			}

			proxy.Start(10000)
			p.Ready()
			<-p.Shutdown()
			// there is no proxy.Stop()
			return nil
		},
	})

	sup.Supervise(&supervisor.Worker{
		Name:     "search-override",
		Requires: []string{"interceptor"},
		Work: func(p *supervisor.Process) error {
			restore := dns.OverrideSearchDomains(".")
			p.Ready()
			<-p.Shutdown()
			restore()
			dns.Flush()
			return nil
		},
	})

	return nil
}

func bridges(p *supervisor.Process, kubeinfo *k8s.KubeInfo) error {
	sup := p.Supervisor()

	connect(p, kubeinfo)

	sup.Supervise(&supervisor.Worker{
		Name: "kubernetes-bridge",
		Work: func(p *supervisor.Process) error {
			// setup kubernetes bridge
			p.Logf("kubernetes ctx=%s ns=%s", kubeinfo.Context, kubeinfo.Namespace)
			var w *k8s.Watcher

			ok := p.Do(func() {
				w = k8s.NewClient(kubeinfo).Watcher()

				updateTable := func(w *k8s.Watcher) {
					table := route.Table{Name: "kubernetes"}

					for _, svc := range w.List("services") {
						ip, ok := svc.Spec()["clusterIP"]
						// for headless services the IP is None, we
						// should properly handle these by listening
						// for endpoints and returning multiple A
						// records at some point
						if ok && ip != "None" {
							qualName := svc.Name() + "." + svc.Namespace() + ".svc.cluster.local"
							table.Add(route.Route{
								Name:   qualName,
								Ip:     ip.(string),
								Proto:  "tcp",
								Target: PROXY_REDIR_PORT,
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
								Target: PROXY_REDIR_PORT,
							})
						}
					}

					post(table)
				}

				w.Watch("services", func(w *k8s.Watcher) {
					updateTable(w)
				})

				w.Watch("pods", func(w *k8s.Watcher) {
					updateTable(w)
				})
			})

			if ok {
				w.Start()
				p.Ready()
				<-p.Shutdown()
				w.Stop()
			}
			return nil
		},
	})

	// Set up DNS search path based on current Kubernetes namespace
	paths := []string{
		kubeinfo.Namespace + ".svc.cluster.local.",
		"svc.cluster.local.",
		"cluster.local.",
		"",
	}
	log.Println("BRG: Setting DNS search path:", paths[0])
	body, err := json.Marshal(paths)
	if err != nil {
		panic(err)
	}
	_, err = http.Post("http://teleproxy/api/search", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("BRG: error setting up search path: %v", err)
		panic(err) // Because this will fail if we win the startup race
	}

	sup.Supervise(&supervisor.Worker{
		Name: "docker-bridge",
		Work: func(p *supervisor.Process) error {
			// setup docker bridge
			dw := docker.NewWatcher()
			dw.Start(func(w *docker.Watcher) {
				table := route.Table{Name: "docker"}
				for name, ip := range w.Containers {
					table.Add(route.Route{Name: name, Ip: ip, Proto: "tcp"})
				}
				post(table)
			})
			p.Ready()
			<-p.Shutdown()
			dw.Stop()
			return nil
		},
	})

	return nil
}

func post(tables ...route.Table) {
	names := make([]string, len(tables))
	for i, t := range tables {
		names[i] = t.Name
	}
	jnames := strings.Join(names, ", ")

	body, err := json.Marshal(tables)
	if err != nil {
		panic(err)
	}
	resp, err := http.Post("http://teleproxy/api/tables/", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("BRG: error posting update to %s: %v", jnames, err)
	} else {
		log.Printf("BRG: posted update to %s: %v", jnames, resp.StatusCode)
	}
}

const TELEPROXY_POD = `
---
apiVersion: v1
kind: Pod
metadata:
  name: teleproxy
  labels:
    name: teleproxy
spec:
  containers:
  - name: proxy
    image: datawire/telepresence-k8s:0.75
    ports:
    - protocol: TCP
      containerPort: 8022
`

func connect(p *supervisor.Process, kubeinfo *k8s.KubeInfo) {
	sup := p.Supervisor()

	// XXX: the dependencies here are correct, but the keeper
	// interface is async, so they don't actually accomplish
	// anything... this will change when keeper dies...

	sup.Supervise(&supervisor.Worker{
		Name: "teleproxy-pod",
		Work: func(p *supervisor.Process) error {
			// setup remote teleproxy pod
			apply := tpu.NewKeeper("KAP", "kubectl "+kubeinfo.GetKubectl("apply -f -"))
			apply.Input = TELEPROXY_POD
			apply.Limit = 1
			apply.Start()
			p.Do(func() {
				apply.Wait()
				p.Ready()
			})
			<-p.Shutdown()
			apply.Stop()
			return nil
		},
	})

	sup.Supervise(&supervisor.Worker{
		Name:     "port-forward",
		Requires: []string{"teleproxy-pod"},
		Work: func(p *supervisor.Process) error {
			pf := tpu.NewKeeper("KPF", "kubectl "+kubeinfo.GetKubectl("port-forward pod/teleproxy 8022"))
			pf.Inspect = "kubectl " + kubeinfo.GetKubectl("get pod/teleproxy")
			pf.Start()
			p.Ready()
			<-p.Shutdown()
			pf.Stop()
			return nil
		},
	})

	sup.Supervise(&supervisor.Worker{
		Name:     "ssh-tunnel",
		Requires: []string{"port-forward"},
		Work: func(p *supervisor.Process) error {
			// XXX: probably need some kind of keepalive check for ssh, first
			// curl after wakeup seems to trigger detection of death
			ssh := tpu.NewKeeper("SSH", "ssh -D localhost:1080 -C -N -oConnectTimeout=5 -oExitOnForwardFailure=yes "+
				"-oStrictHostKeyChecking=no -oUserKnownHostsFile=/dev/null telepresence@localhost -p 8022")
			ssh.Start()
			p.Ready()
			<-p.Shutdown()
			ssh.Stop()
			return nil
		},
	})
}
