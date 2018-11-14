package main

import (
	"bytes"
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

	"github.com/datawire/teleproxy/pkg/k8s"
	"github.com/datawire/teleproxy/pkg/k8s/watcher"

	"github.com/datawire/teleproxy/internal/pkg/api"
	"github.com/datawire/teleproxy/internal/pkg/dns"
	"github.com/datawire/teleproxy/internal/pkg/docker"
	"github.com/datawire/teleproxy/internal/pkg/interceptor"
	"github.com/datawire/teleproxy/internal/pkg/proxy"
	"github.com/datawire/teleproxy/internal/pkg/route"
	"github.com/datawire/teleproxy/internal/pkg/tpu"
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

const (
	DEFAULT   = ""
	INTERCEPT = "intercept"
	BRIDGE    = "bridge"
)

func main() {
	var mode = flag.String("mode", "", "mode of operation")
	var kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	var context = flag.String("context", "", "context to use (default: the current context)")
	var namespace = flag.String("namespace", "", "namespace to use (default: the current namespace for the context")
	var dnsIP = flag.String("dns", "", "dns ip address")
	var fallbackIP = flag.String("fallback", "", "dns fallback")

	flag.Parse()

	checkKubectl()

	switch *mode {
	case "":
	case INTERCEPT:
	case BRIDGE:
		break
	default:
		log.Fatalf("TPY: unrecognized mode: %v", *mode)
	}

	// do this up front so we don't miss out on cleanup if someone
	// Control-C's just after starting us
	signalChan := make(chan os.Signal)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)

	if *mode == DEFAULT || *mode == INTERCEPT {
		shutdown := intercept(dnsIP, fallbackIP)
		defer shutdown()
	}
	if *mode == DEFAULT || *mode == BRIDGE {
		kubeinfo, err := k8s.NewKubeInfo(*kubeconfig, *context, *namespace)
		if err != nil {
			log.Fatalln("KubeInfo failed:", err)
		}
		shutdown := bridges(kubeinfo)
		defer shutdown()
	}

	log.Printf("TPY: %v", <-signalChan)
}

func kubeDie(err error) {
	if err != nil {
		log.Println(err)
	}
	log.Fatal("kubectl version 1.10 or greater is required")
}

func checkKubectl() {
	output, err := tpu.Shell("kubectl version --client -o json")
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

func intercept(dnsIP, fallbackIP *string) func() {
	if *dnsIP == "" {
		dat, err := ioutil.ReadFile("/etc/resolv.conf")
		if err != nil {
			panic(err)
		}
		for _, line := range strings.Split(string(dat), "\n") {
			if strings.Contains(line, "nameserver") {
				fields := strings.Fields(line)
				*dnsIP = fields[1]
				log.Printf("TPY: Automatically set -dns to %v", *dnsIP)
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

	apis, err := api.NewAPIServer(iceptor)
	if err != nil {
		panic(fmt.Sprintf("API Server: %v", err))
	}

	srv := dns.Server{
		Listeners: dnsListeners("1233"),
		Fallback:  *fallbackIP + ":53",
		Resolve: func(domain string) string {
			route := iceptor.Resolve(domain)
			if route != nil {
				return route.Ip
			} else {
				return ""
			}
		},
	}

	// hmm, we may not actually need to get the original
	// destination, we could just forward each ip to a unique port
	// and either listen on that port or run port-forward
	proxy, err := proxy.NewProxy(":1234", iceptor.Destination)
	if err != nil {
		panic(err)
	}

	bootstrap := route.Table{Name: "bootstrap"}
	bootstrap.Add(route.Route{
		Ip:     *dnsIP,
		Target: "1233",
		Proto:  "udp",
	})
	bootstrap.Add(route.Route{
		Name:   "teleproxy",
		Ip:     "127.254.254.254",
		Target: apis.Port(),
		Proto:  "tcp",
	})

	apis.Start()
	srv.Start()
	proxy.Start(10000)
	restore := dns.OverrideSearchDomains(".")

	iceptor.Start()
	iceptor.Update(bootstrap)

	return func() {
		// stop the api server first since it makes calls into
		// the interceptor
		apis.Stop()
		iceptor.Stop()
		restore()
		dns.Flush()
	}
}

func bridges(kubeinfo *k8s.KubeInfo) func() {
	disconnect := connect(kubeinfo)

	// setup kubernetes bridge
	log.Printf("BRG: kubernetes ctx=%s ns=%s", kubeinfo.Context, kubeinfo.Namespace)
	w := watcher.NewWatcher(kubeinfo)
	w.Watch("services", func(w *watcher.Watcher) {
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
					Target: "1234",
				})
			}
		}
		post(table)
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

	// setup docker bridge
	dw := docker.NewWatcher()
	dw.Start(func(w *docker.Watcher) {
		table := route.Table{Name: "docker"}
		for name, ip := range w.Containers {
			table.Add(route.Route{Name: name, Ip: ip, Proto: "tcp"})
		}
		post(table)
	})

	return func() {
		dw.Stop()
		w.Stop()
		post(route.Table{Name: "kubernetes"}, route.Table{Name: "docker"})
		disconnect()
	}
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

func connect(kubeinfo *k8s.KubeInfo) func() {
	// setup remote teleproxy pod
	apply := tpu.NewKeeper("KAP", "kubectl "+kubeinfo.GetKubectl("apply -f -"))
	apply.Input = TELEPROXY_POD
	apply.Limit = 1
	apply.Start()
	apply.Wait()

	pf := tpu.NewKeeper("KPF", "kubectl "+kubeinfo.GetKubectl("port-forward pod/teleproxy 8022"))
	pf.Inspect = "kubectl " + kubeinfo.GetKubectl("get pod/teleproxy")

	// XXX: probably need some kind of keepalive check for ssh, first
	// curl after wakeup seems to trigger detection of death
	ssh := tpu.NewKeeper("SSH", "ssh -D localhost:1080 -C -N -oConnectTimeout=5 -oExitOnForwardFailure=yes "+
		"-oStrictHostKeyChecking=no -oUserKnownHostsFile=/dev/null telepresence@localhost -p 8022")

	pf.Start()
	ssh.Start()

	return func() {
		ssh.Stop()
		pf.Stop()
	}
}
