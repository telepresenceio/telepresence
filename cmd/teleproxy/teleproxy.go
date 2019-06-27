package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"git.lukeshu.com/go/libsystemd/sd_daemon"
	"github.com/pkg/errors"

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/supervisor"

	"github.com/datawire/teleproxy/internal/pkg/api"
	"github.com/datawire/teleproxy/internal/pkg/dns"
	"github.com/datawire/teleproxy/internal/pkg/docker"
	"github.com/datawire/teleproxy/internal/pkg/interceptor"
	"github.com/datawire/teleproxy/internal/pkg/proxy"
	"github.com/datawire/teleproxy/internal/pkg/route"
)

func dnsListeners(p *supervisor.Process, port string) (listeners []string) {
	// turns out you need to listen on localhost for nat to work
	// properly for udp, otherwise you get an "unexpected source
	// blah thingy" because the dns reply packets look like they
	// are coming from the wrong place
	listeners = append(listeners, "127.0.0.1:"+port)

	if runtime.GOOS == "linux" {
		// This is the default docker bridge. We need to listen here because the nat logic we use to intercept
		// dns packets will divert the packet to the interface it originates from, which in the case of
		// containers is the docker bridge. Without this dns won't work from inside containers.
		output, err := p.Command("docker", "inspect", "bridge",
			"-f", "{{(index .IPAM.Config 0).Gateway}}").Capture(nil)
		if err != nil {
			p.Log("not listening on docker bridge")
			return
		}
		listeners = append(listeners, fmt.Sprintf("%s:%s", strings.TrimSpace(output), port))
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
	os.Exit(_main())
}

// worker names
const (
	TELEPROXY       = "TPY"
	TRANSLATOR      = "NAT"
	PROXY           = "PXY"
	API             = "API"
	BRIDGE_WORKER   = "BRG"
	K8S_BRIDGE      = "K8S"
	K8S_PORTFORWARD = "KPF"
	K8S_SSH         = "SSH"
	K8S_APPLY       = "KAP"
	DKR_BRIDGE      = "DKR"
	DNS_SERVER      = "DNS"
	DNS_CONFIG      = "CFG"
	CHECK_READY     = "RDY"
	SIGNAL          = "SIG"
)

var LOG_LEGEND = []struct {
	Prefix      string
	Description string
}{
	{TELEPROXY, "The setup worker launches all the other workers."},
	{TRANSLATOR, "The network address translator controls the system firewall settings used to intercept ip addresses."},
	{PROXY, "The proxy forwards connections to intercepted addresses to the configured destinations."},
	{API, "The API handles requests that allow viewing and updating the routing table that maintains the set of dns names and ip addresses that should be intercepted."},
	{BRIDGE_WORKER, "The bridge worker sets up the kubernetes and docker bridges."},
	{K8S_BRIDGE, "The kubernetes bridge."},
	{K8S_PORTFORWARD, "The kubernetes port forward used for connectivity."},
	{K8S_SSH, "The SSH port forward used on top of the kubernetes port forward."},
	{K8S_APPLY, "The kubernetes apply used to setup the in-cluster pod we talk with."},
	{DKR_BRIDGE, "The docker bridge."},
	{DNS_SERVER, "The DNS server teleproxy runs to intercept dns requests."},
	{CHECK_READY, "The worker teleproxy uses to do a self check and signal the system it is ready."},
}

type Args struct {
	mode       string
	kubeconfig string
	context    string
	namespace  string
	dnsIP      string
	fallbackIP string
	nosearch   bool
	nocheck    bool
	version    bool
}

func _main() int {
	args := Args{}

	flag.BoolVar(&args.version, "version", false, "alias for '-mode=version'")
	flag.StringVar(&args.mode, "mode", "", "mode of operation ('intercept', 'bridge', or 'version')")
	flag.StringVar(&args.kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&args.context, "context", "", "context to use (default: the current context)")
	flag.StringVar(&args.namespace, "namespace", "", "namespace to use (default: the current namespace for the context")
	flag.StringVar(&args.dnsIP, "dns", "", "dns ip address")
	flag.StringVar(&args.fallbackIP, "fallback", "", "dns fallback")
	flag.BoolVar(&args.nosearch, "noSearchOverride", false, "disable dns search override")
	flag.BoolVar(&args.nocheck, "noCheck", false, "disable self check")

	flag.Parse()

	if args.version {
		args.mode = VERSION
	}

	switch args.mode {
	case DEFAULT, INTERCEPT, BRIDGE:
		// do nothing
	case VERSION:
		fmt.Println("teleproxy", "version", Version)
		return 0
	default:
		panic(fmt.Sprintf("TPY: unrecognized mode: %v", args.mode))
	}

	// do this up front so we don't miss out on cleanup if someone
	// Control-C's just after starting us
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())
	sup := supervisor.WithContext(ctx)

	sup.Supervise(&supervisor.Worker{
		Name: TELEPROXY,
		Work: func(p *supervisor.Process) error {
			return teleproxy(p, args)
		},
	})

	sup.Supervise(&supervisor.Worker{
		Name: SIGNAL,
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

	log.Println("Log prefixes used by the different teleproxy workers:")
	log.Println("")
	for _, entry := range LOG_LEGEND {
		log.Printf("  %s -> %s\n", entry.Prefix, entry.Description)
	}
	log.Println("")

	errs := sup.Run()
	if len(errs) > 0 {
		fmt.Printf("Teleproxy exited with %d error(s):\n", len(errs))
	} else {
		fmt.Println("Teleproxy exited successfully")
	}

	for _, err := range errs {
		fmt.Printf("  %v\n", err)
	}
	if len(errs) > 0 {
		return 1
	} else {
		return 0
	}
}

func selfcheck(p *supervisor.Process) error {
	// XXX: these checks might not make sense if -dns is specified
	lookupName := fmt.Sprintf("teleproxy%d.cachebust.telepresence.io", time.Now().Unix())
	for _, name := range []string{fmt.Sprintf("%s.", lookupName), lookupName} {
		ips, err := net.LookupIP(name)
		if err != nil {
			return err
		}

		if len(ips) != 1 {
			return errors.Errorf("unexpected ips for %s: %v", name, ips)
		}

		if !ips[0].Equal(net.ParseIP(MAGIC_IP)) {
			return errors.Errorf("found wrong ip for %s: %v", name, ips)
		}

		p.Logf("%s resolves to %v", name, ips)
	}

	curl := p.Command("curl", "-sqI", fmt.Sprintf("%s/api/tables/", lookupName))
	err := curl.Start()
	if err != nil {
		return err
	}

	return p.DoClean(curl.Wait, curl.Process.Kill)
}

func teleproxy(p *supervisor.Process, args Args) error {
	sup := p.Supervisor()

	if args.mode == DEFAULT || args.mode == INTERCEPT {
		err := intercept(p, args)
		if err != nil {
			return err
		}
		sup.Supervise(&supervisor.Worker{
			Name:     CHECK_READY,
			Requires: []string{TRANSLATOR, API, DNS_SERVER, PROXY, DNS_CONFIG},
			Work: func(p *supervisor.Process) error {
				err := selfcheck(p)
				if err != nil {
					if args.nocheck {
						p.Logf("WARNING, SELF CHECK FAILED: %v", err)
					} else {
						return errors.Wrap(err, "SELF CHECK FAILED")
					}
				} else {
					p.Logf("SELF CHECK PASSED, SIGNALING READY")
				}

				err = p.Do(func() error {
					if err := (sd_daemon.Notification{State: "READY=1"}).Send(false); err != nil {
						p.Logf("Ignoring daemon notification failure: %v", err)
					}
					p.Ready()
					return nil
				})
				if err != nil {
					return err
				}

				<-p.Shutdown()
				return nil
			},
		})
	}

	if args.mode == DEFAULT || args.mode == BRIDGE {
		requires := []string{}
		if args.mode != BRIDGE {
			requires = append(requires, CHECK_READY)
		}
		sup.Supervise(&supervisor.Worker{
			Name:     BRIDGE_WORKER,
			Requires: requires,
			Work: func(p *supervisor.Process) error {
				err := checkKubectl(p)
				if err != nil {
					return err
				}

				kubeinfo := k8s.NewKubeInfo(args.kubeconfig, args.context, args.namespace)
				bridges(p, kubeinfo)
				return nil
			},
		})
	}

	return nil
}

const KUBECTL_ERR = "kubectl version 1.10 or greater is required"

func checkKubectl(p *supervisor.Process) error {
	output, err := p.Command("kubectl", "version", "--client", "-o", "json").Capture(nil)
	if err != nil {
		return errors.Wrap(err, KUBECTL_ERR)
	}

	var info struct {
		ClientVersion struct {
			Major string
			Minor string
		}
	}

	err = json.Unmarshal([]byte(output), &info)
	if err != nil {
		return errors.Wrap(err, KUBECTL_ERR)
	}

	major, err := strconv.Atoi(info.ClientVersion.Major)
	if err != nil {
		return errors.Wrap(err, KUBECTL_ERR)
	}
	minor, err := strconv.Atoi(info.ClientVersion.Minor)
	if err != nil {
		return errors.Wrap(err, KUBECTL_ERR)
	}

	if major != 1 || minor < 10 {
		return errors.Errorf("%s (found %d.%d)", KUBECTL_ERR, major, minor)
	}

	return nil
}

// intercept starts the interceptor, and only returns once the
// interceptor is successfully running in another goroutine.  It
// returns a function to call to shut down that goroutine.
//
// If dnsIP is empty, it will be detected from /etc/resolv.conf
//
// If fallbackIP is empty, it will default to Google DNS.
func intercept(p *supervisor.Process, args Args) error {
	if os.Geteuid() != 0 {
		return errors.New("ERROR: teleproxy must be run as root or suid root")
	}

	sup := p.Supervisor()

	if args.dnsIP == "" {
		dat, err := ioutil.ReadFile("/etc/resolv.conf")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(dat), "\n") {
			if strings.Contains(line, "nameserver") {
				fields := strings.Fields(line)
				args.dnsIP = fields[1]
				log.Printf("TPY: Automatically set -dns=%v", args.dnsIP)
				break
			}
		}
	}
	if args.dnsIP == "" {
		return errors.New("couldn't determine dns ip from /etc/resolv.conf")
	}

	if args.fallbackIP == "" {
		if args.dnsIP == "8.8.8.8" {
			args.fallbackIP = "8.8.4.4"
		} else {
			args.fallbackIP = "8.8.8.8"
		}
		log.Printf("TPY: Automatically set -fallback=%v", args.fallbackIP)
	}
	if args.fallbackIP == args.dnsIP {
		return errors.New("if your fallbackIP and your dnsIP are the same, you will have a dns loop")
	}

	iceptor := interceptor.NewInterceptor("teleproxy")
	apis, err := api.NewAPIServer(iceptor)
	if err != nil {
		return errors.Wrap(err, "API Server")
	}

	sup.Supervise(&supervisor.Worker{
		Name: TRANSLATOR,
		// XXX: Requires will need to include the api server once it is changed to not bind early
		Requires: []string{PROXY, DNS_SERVER},
		Work:     iceptor.Work,
	})

	sup.Supervise(&supervisor.Worker{
		Name:     API,
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
		Name:     DNS_SERVER,
		Requires: []string{},
		Work: func(p *supervisor.Process) error {
			srv := dns.Server{
				Listeners: dnsListeners(p, DNS_REDIR_PORT),
				Fallback:  args.fallbackIP + ":53",
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
		Name:     PROXY,
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
		Name:     DNS_CONFIG,
		Requires: []string{TRANSLATOR},
		Work: func(p *supervisor.Process) error {
			bootstrap := route.Table{Name: "bootstrap"}
			bootstrap.Add(route.Route{
				Ip:     args.dnsIP,
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

			var restore func()
			if !args.nosearch {
				restore = dns.OverrideSearchDomains(p, ".")
			}

			p.Ready()
			<-p.Shutdown()

			if !args.nosearch {
				restore()
			}

			dns.Flush()
			return nil
		},
	})

	return nil
}

var (
	ABORTED = errors.New("aborted")
)

func bridges(p *supervisor.Process, kubeinfo *k8s.KubeInfo) error {
	sup := p.Supervisor()

	connect(p, kubeinfo)

	sup.Supervise(&supervisor.Worker{
		Name: K8S_BRIDGE,
		Work: func(p *supervisor.Process) error {
			// setup kubernetes bridge
			ctx, err := kubeinfo.Context()
			if err != nil {
				return err
			}
			ns, err := kubeinfo.Namespace()
			if err != nil {
				return err
			}
			p.Logf("kubernetes ctx=%s ns=%s", ctx, ns)
			var w *k8s.Watcher

			err = p.DoClean(func() error {
				var err error
				w, err = k8s.NewWatcher(kubeinfo)
				if err != nil {
					return err
				}

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
				return nil
			}, func() error {
				return ABORTED
			})

			if err == ABORTED {
				return nil
			}

			if err != nil {
				return err
			}

			w.Start()
			p.Ready()
			<-p.Shutdown()
			w.Stop()

			return nil
		},
	})

	// Set up DNS search path based on current Kubernetes namespace
	namespace, err := kubeinfo.Namespace()
	if err != nil {
		return err
	}
	paths := []string{
		namespace + ".svc.cluster.local.",
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
		Name: DKR_BRIDGE,
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

	sup.Supervise(&supervisor.Worker{
		Name: K8S_APPLY,
		Work: func(p *supervisor.Process) (err error) {
			// setup remote teleproxy pod
			args, err := kubeinfo.GetKubectlArray("apply", "-f", "-")
			if err != nil {
				return err
			}
			apply := p.Command("kubectl", args...)
			apply.Stdin = strings.NewReader(TELEPROXY_POD)
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

	sup.Supervise(&supervisor.Worker{
		Name:     K8S_PORTFORWARD,
		Requires: []string{K8S_APPLY},
		Retry:    true,
		Work: func(p *supervisor.Process) (err error) {
			args, err := kubeinfo.GetKubectlArray("port-forward", "pod/teleproxy", "8022")
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
					args, err := kubeinfo.GetKubectlArray("get", "pod/teleproxy")
					if err != nil {
						return err
					}
					inspect := p.Command("kubectl", args...)
					inspect.Run()
				}
				return err
			}, func() error {
				return pf.Process.Kill()
			})
			return
		},
	})

	sup.Supervise(&supervisor.Worker{
		Name:     K8S_SSH,
		Requires: []string{K8S_PORTFORWARD},
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
