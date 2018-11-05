package main

import (
	"flag"
	"io/ioutil"
	"log"
	"os"
	"os/user"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/datawire/teleproxy/internal/pkg/dns"
	"github.com/datawire/teleproxy/internal/pkg/docker"
	"github.com/datawire/teleproxy/internal/pkg/interceptor"
	"github.com/datawire/teleproxy/internal/pkg/k8s/watcher"
	"github.com/datawire/teleproxy/internal/pkg/proxy"
	"github.com/datawire/teleproxy/internal/pkg/route"
	"github.com/datawire/teleproxy/internal/pkg/tpu"
)

var kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
var dnsIP = flag.String("dns", "", "dns ip address")
var fallbackIP = flag.String("fallback", "", "dns fallback")

func dnsListeners(port string) (listeners []string) {
	// turns out you need to listen on localhost for nat to work
	// properly for udp, otherwise you get an "unexpected source
	// blah thingy" because the dns reply packets look like they
	// are coming from the wrong place
	listeners = append(listeners, "127.0.0.1:" + port)

	if runtime.GOOS == "linux" {
		// This is the default docker bridge. We should
		// probably figure out how to query this out of docker
		// instead of hardcoding it. We need to listen here
		// because the nat logic we use to intercept dns
		// packets will divert the packet to the interface it
		// originates from, which in the case of containers is
		// the docker bridge. Without this dns won't work from
		// inside containers.
		listeners = append(listeners, "172.17.0.1:" + port)
	}

	return
}

func main() {
	flag.Parse()

	if *kubeconfig == "" {
		*kubeconfig = os.Getenv("KUBECONFIG")
	}

	if *kubeconfig == "" {
		current, err := user.Current()
		if err != nil { panic(err) }
		home := current.HomeDir
		*kubeconfig = filepath.Join(home, ".kube/config")
	}

	if *dnsIP == "" {
		dat, err := ioutil.ReadFile("/etc/resolv.conf")
		if err != nil { panic(err) }
		for _, line := range strings.Split(string(dat), "\n") {
			if strings.Contains(line, "nameserver") {
				fields := strings.Fields(line)
				*dnsIP = fields[1]
				log.Printf("Automatically set -dns to %v", *dnsIP)
				break
			}
		}
	}

	if *dnsIP == "" {
		panic("couldn't determine dns ip from /etc/resolv.conf")
	}

	if *fallbackIP == "" {
		if *dnsIP == "8.8.8.8" {
			*fallbackIP = "8.8.4.4"
		} else {
			*fallbackIP = "8.8.8.8"
		}
	}

	if *fallbackIP == *dnsIP {
		panic("if your fallbackIP and your dnsIP are the same, you will have a dns loop")
	}

	iceptor := interceptor.NewInterceptor("teleproxy")

	srv := dns.Server{
		Listeners: dnsListeners("1233"),
		Fallback: *fallbackIP + ":53",
		Resolve: func(domain string) string {
			route := iceptor.Resolve(domain)
			if route != nil {
				return route.Ip
			} else {
				return ""
			}
		},
	}
	srv.Start()

	// hmm, we may not actually need to get the original
	// destination, we could just forward each ip to a unique port
	// and either listen on that port or run port-forward
	proxy, err := proxy.NewProxy(":1234", iceptor.Destination)
	if err != nil {
		log.Println(err)
		return
	}
	proxy.Start(10000)

	bootstrap := route.Table{Name: "bootstrap"}
	bootstrap.Add(route.Route{
		Ip: *dnsIP,
		Target: "1233",
		Proto: "udp",
	})
	bootstrap.Add(route.Route{
		Name: "teleproxy",
		Ip: "1.2.3.4",
		Proto: "tcp",
	})

	restore := dns.OverrideSearchDomains(".")
	defer restore()

	iceptor.Start()
	defer iceptor.Stop()
	iceptor.Update(bootstrap)

	disconnect := connect()
	defer disconnect()

	// setup kubernetes bridge
	w := watcher.NewWatcher(*kubeconfig)
	defer w.Stop()
	w.Watch("services", func(w *watcher.Watcher) {
		table := route.Table{Name: "kubernetes"}
		for _, svc := range w.List("services") {
			ip, ok := svc.Spec()["clusterIP"]
			if ok {
				table.Add(route.Route{
					Name: svc.Name(),
					Ip: ip.(string),
					Proto: "tcp",
					Target: "1234",
				})
			}
		}
		iceptor.Update(table)
		dns.Flush()
	})

	// setup docker bridge
	dw := docker.NewWatcher()
	dw.Start(func(w *docker.Watcher) {
		table := route.Table{Name: "docker"}
		for name, ip := range w.Containers {
			table.Add(route.Route{Name: name, Ip: ip, Proto: "tcp"})
		}
		// this sometimes panics with a send on a closed channel
		iceptor.Update(table)
		dns.Flush()
	})
	defer dw.Stop()

	ch := make(chan os.Signal)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	log.Println(<-ch)

	defer dns.Flush()
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

func connect() func() {
	// setup remote teleproxy pod
	apply := tpu.Keepalive(1, TELEPROXY_POD, "kubectl", "--kubeconfig", *kubeconfig, "apply", "-f", "-")
	apply.Wait()

	pf := tpu.Keepalive(0, "", "kubectl", "--kubeconfig", *kubeconfig, "port-forward", "pod/teleproxy", "8022")
	// XXX: probably need some kind of keepalive check for ssh, first
	// curl after wakeup seems to trigger detection of death
	ssh := tpu.Keepalive(0, "", "ssh", "-D", "localhost:1080", "-C", "-N", "-oConnectTimeout=5", "-oExitOnForwardFailure=yes",
		"-oStrictHostKeyChecking=no", "-oUserKnownHostsFile=/dev/null", "telepresence@localhost", "-p", "8022")
	return func() {
		ssh.Shutdown()
		pf.Shutdown()
	}
}
