package teleproxy

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/datawire/ambassador/pkg/k8s"
	"github.com/datawire/ambassador/pkg/supervisor"

	"github.com/datawire/ambassador/internal/pkg/api"
	"github.com/datawire/ambassador/internal/pkg/dns"
	"github.com/datawire/ambassador/internal/pkg/docker"
	"github.com/datawire/ambassador/internal/pkg/interceptor"
	"github.com/datawire/ambassador/internal/pkg/proxy"
	"github.com/datawire/ambassador/internal/pkg/route"
)

func dnsListeners(p *supervisor.Process, port string) (listeners []string) {
	// turns out you need to listen on localhost for nat to work
	// properly for udp, otherwise you get an "unexpected source
	// blah thingy" because the dns reply packets look like they
	// are coming from the wrong place
	listeners = append(listeners, "127.0.0.1:"+port)

	_, err := os.Stat("/.dockerenv")
	insideDocker := err == nil

	if runtime.GOOS == "linux" && !insideDocker {
		// This is the default docker bridge. We need to listen here because the nat logic we use to intercept
		// dns packets will divert the packet to the interface it originates from, which in the case of
		// containers is the docker bridge. Without this dns won't work from inside containers.
		output, err := p.Command("docker", "inspect", "bridge",
			"-f", "{{(index .IPAM.Config 0).Gateway}}").Capture(nil)
		if err != nil {
			p.Log("not listening on docker bridge")
			return
		}
		extraIP := strings.TrimSpace(output)
		if extraIP != "127.0.0.1" && extraIP != "0.0.0.0" && extraIP != "" {
			listeners = append(listeners, fmt.Sprintf("%s:%s", extraIP, port))
		}
	}

	return
}

const (
	defaultMode   = ""
	interceptMode = "intercept"
	bridgeMode    = "bridge"
	versionMode   = "version"

	// DNSRedirPort is the port to which we redirect dns requests. It
	// should probably eventually be configurable and/or dynamically
	// chosen
	DNSRedirPort = "1233"

	// ProxyRedirPort is the port to which we redirect proxied IPs. It
	// should probably eventually be configurable and/or dynamically
	// chosen.
	ProxyRedirPort = "1234"

	// MagicIP is an IP from the localhost range that we resolve
	// "teleproxy" to and intercept for convenient access to the
	// teleproxy api server. This enables things like `curl
	// teleproxy/api/tables/`. In theory this could be any arbitrary
	// value that is unlikely to conflict with a real world IP, but it
	// is also handy for it to be fixed so that we can debug even if
	// DNS isn't working by doing stuff like `curl
	// 127.254.254.254/api/...`. This value happens to be the last
	// value in the IPv4 localhost range.
	MagicIP = "127.254.254.254"
)

// worker names
const (
	TeleproxyWorker      = "TPY"
	TranslatorWorker     = "NAT"
	ProxyWorker          = "PXY"
	APIWorker            = "API"
	BridgeWorker         = "BRG"
	K8sBridgeWorker      = "K8S"
	K8sPortForwardWorker = "KPF"
	K8sSSHWorker         = "SSH"
	K8sApplyWorker       = "KAP"
	DkrBridgeWorker      = "DKR"
	DNSServerWorker      = "DNS"
	DNSConfigWorker      = "CFG"
	CheckReadyWorker     = "RDY"
	SignalWorker         = "SIG"
)

var logLegend = []struct {
	Prefix      string
	Description string
}{
	{TeleproxyWorker, "The setup worker launches all the other workers."},
	{TranslatorWorker, "The network address translator controls the system firewall settings used to " +
		"intercept ip addresses."},
	{ProxyWorker, "The proxy forwards connections to intercepted addresses to the configured destinations."},
	{APIWorker, "The API handles requests that allow viewing and updating the routing table that maintains " +
		"the set of dns names and ip addresses that should be intercepted."},
	{BridgeWorker, "The bridge worker sets up the kubernetes and docker bridges."},
	{K8sBridgeWorker, "The kubernetes bridge."},
	{K8sPortForwardWorker, "The kubernetes port forward used for connectivity."},
	{K8sSSHWorker, "The SSH port forward used on top of the kubernetes port forward."},
	{K8sApplyWorker, "The kubernetes apply used to setup the in-cluster pod we talk with."},
	{DkrBridgeWorker, "The docker bridge."},
	{DNSServerWorker, "The DNS server teleproxy runs to intercept dns requests."},
	{CheckReadyWorker, "The worker teleproxy uses to do a self check and signal the system it is ready."},
}

// Teleproxy holds the configuration for this Teleproxy invocation
type Teleproxy struct {
	Mode       string
	Kubeconfig string
	Context    string
	Namespace  string
	DNSIP      string
	FallbackIP string
	NoSearch   bool
	NoCheck    bool
	Version    bool
	supervisor *supervisor.Supervisor
	workers    []*supervisor.Worker
}

// RunTeleproxy is the main entry point for Teleproxy
func RunTeleproxy(tele *Teleproxy, version string) error {
	if tele.Version {
		tele.Mode = versionMode
	}

	switch tele.Mode {
	case defaultMode, interceptMode, bridgeMode:
		// do nothing
	case versionMode:
		fmt.Println("teleproxy", "version", version)
		return nil
	default:
		return errors.Errorf("TPY: unrecognized mode: %v", tele.Mode)
	}

	// do this up front so we don't miss out on cleanup if someone
	// Control-C's just after starting us
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	ctx, cancel := context.WithCancel(context.Background())
	sup := supervisor.WithContext(ctx)
	tele.supervisor = sup

	sup.Supervise(&supervisor.Worker{
		Name: TeleproxyWorker,
		Work: func(p *supervisor.Process) error {
			return teleproxy(p, tele)
		},
	})

	sup.Supervise(&supervisor.Worker{
		Name: SignalWorker,
		Work: func(p *supervisor.Process) error {
			for {
				select {
				case <-p.Shutdown():
					return nil
				case s := <-signalChan:
					p.Logf("TPY: %v", s)
					if s == syscall.SIGHUP {
						for _, w := range tele.workers {
							w.Shutdown()
						}
						for _, w := range tele.workers {
							w.Wait()
						}
						for _, w := range tele.workers {
							w.Restart()
						}
					} else {
						cancel()
						return nil
					}
				}
			}
			return nil
		},
	})

	log.Println("Log prefixes used by the different teleproxy workers:")
	log.Println("")
	for _, entry := range logLegend {
		log.Printf("  %s -> %s\n", entry.Prefix, entry.Description)
	}
	log.Println("")

	errs := sup.Run()
	if len(errs) == 0 {
		fmt.Println("Teleproxy exited successfully")
		return nil
	}

	msg := fmt.Sprintf("Teleproxy exited with %d error(s):\n", len(errs))

	for _, err := range errs {
		msg += fmt.Sprintf("  %v\n", err)
	}

	return errors.New(strings.TrimSpace(msg))
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

		if !ips[0].Equal(net.ParseIP(MagicIP)) {
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

func teleproxy(p *supervisor.Process, tele *Teleproxy) error {
	sup := p.Supervisor()

	if tele.Mode == defaultMode || tele.Mode == interceptMode {
		err := intercept(p, tele)
		if err != nil {
			return err
		}
		sup.Supervise(&supervisor.Worker{
			Name:     CheckReadyWorker,
			Requires: []string{TranslatorWorker, APIWorker, DNSServerWorker, ProxyWorker, DNSConfigWorker},
			Work: func(p *supervisor.Process) error {
				err := selfcheck(p)
				if err != nil {
					if tele.NoCheck {
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

	if tele.Mode == defaultMode || tele.Mode == bridgeMode {
		requires := []string{}
		if tele.Mode != bridgeMode {
			requires = append(requires, CheckReadyWorker)
		}
		sup.Supervise(&supervisor.Worker{
			Name:     BridgeWorker,
			Requires: requires,
			Work: func(p *supervisor.Process) error {
				err := checkKubectl(p)
				if err != nil {
					return err
				}

				bridges(p, tele)
				return nil
			},
		})
	}

	return nil
}

func (t *Teleproxy) addWorker(worker *supervisor.Worker) {
	t.supervisor.Supervise(worker)
	t.workers = append(t.workers, worker)
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

// intercept starts the interceptor, and only returns once the
// interceptor is successfully running in another goroutine.  It
// returns a function to call to shut down that goroutine.
//
// If dnsIP is empty, it will be detected from /etc/resolv.conf
//
// If fallbackIP is empty, it will default to Google DNS.
func intercept(p *supervisor.Process, tele *Teleproxy) error {
	if os.Geteuid() != 0 {
		return errors.New("ERROR: teleproxy must be run as root or suid root")
	}

	sup := p.Supervisor()

	if tele.DNSIP == "" {
		dat, err := ioutil.ReadFile("/etc/resolv.conf")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(dat), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "nameserver") {
				fields := strings.Fields(line)
				tele.DNSIP = fields[1]
				log.Printf("TPY: Automatically set -dns=%v", tele.DNSIP)
				break
			}
		}
	}
	if tele.DNSIP == "" {
		return errors.New("couldn't determine dns ip from /etc/resolv.conf")
	}

	if tele.FallbackIP == "" {
		if tele.DNSIP == "8.8.8.8" {
			tele.FallbackIP = "8.8.4.4"
		} else {
			tele.FallbackIP = "8.8.8.8"
		}
		log.Printf("TPY: Automatically set -fallback=%v", tele.FallbackIP)
	}
	if tele.FallbackIP == tele.DNSIP {
		return errors.New("if your fallbackIP and your dnsIP are the same, you will have a dns loop")
	}

	iceptor := interceptor.NewInterceptor("teleproxy")
	apis, err := api.NewAPIServer(iceptor)
	if err != nil {
		return errors.Wrap(err, "API Server")
	}

	sup.Supervise(&supervisor.Worker{
		Name: TranslatorWorker,
		// XXX: Requires will need to include the api server once it is changed to not bind early
		Requires: []string{ProxyWorker, DNSServerWorker},
		Work:     iceptor.Work,
	})

	sup.Supervise(&supervisor.Worker{
		Name:     APIWorker,
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
		Name:     DNSServerWorker,
		Requires: []string{},
		Work: func(p *supervisor.Process) error {
			srv := dns.Server{
				Listeners: dnsListeners(p, DNSRedirPort),
				Fallback:  tele.FallbackIP + ":53",
				Resolve: func(domain string) string {
					route := iceptor.Resolve(domain)
					if route != nil {
						return route.Ip
					}
					return ""
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
		Name:     ProxyWorker,
		Requires: []string{},
		Work: func(p *supervisor.Process) error {
			// hmm, we may not actually need to get the original
			// destination, we could just forward each ip to a unique port
			// and either listen on that port or run port-forward
			proxy, err := proxy.NewProxy(fmt.Sprintf(":%s", ProxyRedirPort), iceptor.Destination)
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
		Name:     DNSConfigWorker,
		Requires: []string{TranslatorWorker},
		Work: func(p *supervisor.Process) error {
			bootstrap := route.Table{Name: "bootstrap"}
			bootstrap.Add(route.Route{
				Ip:     tele.DNSIP,
				Target: DNSRedirPort,
				Proto:  "udp",
			})
			bootstrap.Add(route.Route{
				Name:   "teleproxy",
				Ip:     MagicIP,
				Target: apis.Port(),
				Proto:  "tcp",
			})
			iceptor.Update(bootstrap)

			var restore func()
			if !tele.NoSearch {
				restore = dns.OverrideSearchDomains(p, ".")
			}

			p.Ready()
			<-p.Shutdown()

			if !tele.NoSearch {
				restore()
			}

			dns.Flush()
			return nil
		},
	})

	return nil
}

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

func bridges(p *supervisor.Process, tele *Teleproxy) {
	sup := p.Supervisor()

	connect(tele)

	tele.addWorker(&supervisor.Worker{
		Name: K8sBridgeWorker,
		Work: func(p *supervisor.Process) error {
			// setup kubernetes bridge

			kubeinfo := k8s.NewKubeInfo(tele.Kubeconfig, tele.Context, tele.Namespace)

			// Set up DNS search path based on current Kubernetes namespace
			namespace, err := kubeinfo.Namespace()
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

			err = p.DoClean(func() error {
				var err error
				w, err = k8s.NewWatcher(kubeinfo)
				if err != nil {
					return err
				}
				return nil
			}, func() error {
				return errAborted
			})

			if err == errAborted {
				return nil
			}

			if err != nil {
				return err
			}
			updateTable := func(w *k8s.Watcher) {
				table := route.Table{Name: "kubernetes"}

				for _, svc := range w.List("services") {
					decoded := svcResource{}
					err := svc.Decode(&decoded)
					if err != nil {
						p.Logf("error decoding service: %v", err)
						continue
					}

					spec := decoded.Spec

					ports := ""
					for _, port := range spec.Ports {
						if ports == "" {
							ports = fmt.Sprintf("%d", port.Port)
						} else {
							ports = fmt.Sprintf("%s,%d", ports, port.Port)
						}
					}

					ip := spec.ClusterIP
					// for headless services the IP is None, we
					// should properly handle these by listening
					// for endpoints and returning multiple A
					// records at some point
					if ip != "" && ip != "None" {
						qualName := svc.Name() + "." + svc.Namespace() + ".svc.cluster.local"
						table.Add(route.Route{
							Name:   qualName,
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
						table.Add(route.Route{
							Name:   qname,
							Ip:     ip.(string),
							Proto:  "tcp",
							Target: ProxyRedirPort,
						})
					}
				}

				post(table)
			}

			startWatches := func(namespace string) {
				if err := w.WatchQuery(k8s.Query{Kind: "services", Namespace: namespace}, updateTable); err != nil {
					// FIXME why do we ignore this error?
					p.Logf("watch services: %+v", err)
				}

				if err := w.WatchQuery(k8s.Query{Kind: "pods", Namespace: namespace}, updateTable); err != nil {
					// FIXME why do we ignore this error?
					p.Logf("watch pods: %+v", err)
				}

				w.Start()
				p.Ready()
				<-p.Shutdown()
				w.Stop()
			}

			// Since w.WatchQuery panics instead of returning errors, wrap the watch setup and startup process in
			// a function call and use go's recovery mechanism to setup fallback watches on a single-namespace.
			watchRecovery := func() {
				if r := recover(); r != nil {
					p.Logf("recovered: %v", r)
					ns, _ := kubeinfo.Namespace()
					if ns == "" {
						ns = "default"
					}
					p.Logf("setting up watches on namespace: %v", ns)
					startWatches(ns)
				}
				return
			}
			defer watchRecovery()
			startWatches(k8s.NamespaceAll)

			return nil
		},
	})

	sup.Supervise(&supervisor.Worker{
		Name: DkrBridgeWorker,
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

const teleproxyPod = `
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

func connect(tele *Teleproxy) {
	tele.addWorker(&supervisor.Worker{
		Name: K8sApplyWorker,
		Work: func(p *supervisor.Process) (err error) {
			kubeinfo := k8s.NewKubeInfo(tele.Kubeconfig, tele.Context, tele.Namespace)
			// setup remote teleproxy pod
			args, err := kubeinfo.GetKubectlArray("apply", "-f", "-")
			if err != nil {
				return err
			}
			apply := p.Command("kubectl", args...)
			apply.Stdin = strings.NewReader(teleproxyPod)
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

	tele.addWorker(&supervisor.Worker{
		Name:     K8sPortForwardWorker,
		Requires: []string{K8sApplyWorker},
		Retry:    true,
		Work: func(p *supervisor.Process) (err error) {

			kubeinfo := k8s.NewKubeInfo(tele.Kubeconfig, tele.Context, tele.Namespace)
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
					_ = inspect.Run() // Discard error as this is just for logging
				}
				return err
			}, func() error {
				return pf.Process.Kill()
			})
			return
		},
	})

	tele.addWorker(&supervisor.Worker{
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
