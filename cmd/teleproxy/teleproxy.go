package main

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/user"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"github.com/datawire/teleproxy/internal/pkg/nat"
	"github.com/datawire/teleproxy/internal/pkg/tpu"
	"github.com/miekg/dns"
	"golang.org/x/net/proxy"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/cache"
)

var kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")

func kubeWatch() {
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err.Error())
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	
	watchlist := cache.NewListWatchFromClient(clientset.Core().RESTClient(), "services", v1.NamespaceAll,
		fields.Everything())
	_, controller := cache.NewInformer(
		watchlist,
		&v1.Service{},
		time.Second * 0,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				svc := obj.(*v1.Service)
				log.Printf("ADDED: %s->%s\n", svc.Name, svc.Spec.ClusterIP)
				updateRoute(svc)
			},
			DeleteFunc: func(obj interface{}) {
				svc := obj.(*v1.Service)
				log.Printf("DELETED: %s\n", svc.Name)
				key := svc.Name + "."
				removeRoute(key)
				domainsToAddresses.Delete(key)
			},
			UpdateFunc:func(oldObj, newObj interface{}) {
				svc := newObj.(*v1.Service)
				log.Printf("CHANGED: %s->%s\n", svc.Name, svc.Spec.ClusterIP)
				updateRoute(svc)
			},
		},
	)
	stop := make(chan struct{})
	go controller.Run(stop)
}


var domainsToAddresses sync.Map
// XXX: need to do better than teleproxy
var translator = nat.NewTranslator("teleproxy")

func removeRoute(key string) {
	if old, ok := domainsToAddresses.Load(key); ok {
		translator.ClearTCP(old.(string))
	}
}

func updateRoute(svc *v1.Service) {
	if svc.Spec.ClusterIP == "None" { return }
	domainsToAddresses.Store(strings.ToLower(svc.Name + "."), svc.Spec.ClusterIP)
	translator.ForwardTCP(svc.Spec.ClusterIP, "1234")
	kickDNS()
}

type handler struct{}
func (this *handler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	log.Println(r.Question[0].Qtype, "DNS request for", r.Question[0].Name)
	domain := strings.ToLower(r.Question[0].Name)
	switch r.Question[0].Qtype {
	case dns.TypeA:
		log.Println("Looking up", domain)
		address, ok := domainsToAddresses.Load(domain)
		if ok {
			log.Println("Found:", domain)
			msg := dns.Msg{}
			msg.SetReply(r)
			msg.Authoritative = true
			// mac dns seems to fallback if you don't
			// support recursion, if you have more than a
			// single dns server, this will prevent us
			// from intercepting all queries
			msg.RecursionAvailable = true
			// if we don't give back the same domain
			// requested, then mac dns seems to return an
			// nxdomain
			msg.Answer = append(msg.Answer, &dns.A{
				Hdr: dns.RR_Header{ Name: r.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60 },
				A: net.ParseIP(address.(string)),
			})
			w.WriteMsg(&msg)
			return
		}
	default:
		_, ok := domainsToAddresses.Load(domain)
		if ok {
			log.Println("Found:", domain)
			msg := dns.Msg{}
			msg.SetReply(r)
			msg.Authoritative = true
			msg.RecursionAvailable = true
			w.WriteMsg(&msg)
			log.Println("replied with empty")
			return
		}
	}
	in, err := dns.Exchange(r, *fallbackIP + ":53")
	if err != nil {
		log.Println(err)
		return
	}
	w.WriteMsg(in)
}

func dnsMain() {
	h := handler{}

	go func() {
		// turns out you need to listen on localhost for nat to work
		// properly for udp, otherwise you get an "unexpected source
		// blah thingy" because the dns reply packets look like they
		// are coming from the wrong place
		srv := &dns.Server{Addr: "127.0.0.1:" + strconv.Itoa(1233), Net: "udp"}
		srv.Handler = &h
		if err := srv.ListenAndServe(); err != nil {
			log.Fatalf("Failed to set udp listener %s\n", err.Error())
		}
	}()

	if runtime.GOOS == "linux" {
		go func() {
			// This is the default docker bridge. We should
			// probably figure out how to query this out of docker
			// instead of hardcoding it. We need to listen here
			// because the nat logic we use to intercept dns
			// packets will divert the packet to the interface it
			// originates from, which in the case of containers is
			// the docker bridge. Without this dns won't work from
			// inside containers.
			srv := &dns.Server{Addr: "172.17.0.1:" + strconv.Itoa(1233), Net: "udp"}
			srv.Handler = &h
			if err := srv.ListenAndServe(); err != nil {
				log.Fatalf("Failed to set udp listener %s\n", err.Error())
			}
		}()
	}
}

func rlimit() {
	var rLimit syscall.Rlimit
	err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		log.Println("Error getting rlimit:", err)
	} else {
		log.Println("Initial rlimit:", rLimit)
	}

	rLimit.Max = 999999
	rLimit.Cur = 999999
	err = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		log.Println("Error setting rlimit:", err)
	}

	err = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit)
	if err != nil {
		log.Println("Error getting rlimit:", err)
	} else {
		log.Println("Final rlimit", rLimit)
	}
}

var dnsIP = flag.String("dns", "", "dns ip address")
var fallbackIP = flag.String("fallback", "", "dns fallback")

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

	if runtime.GOOS == "darwin" {
		ifaces, _ := getIfaces()
		for _, iface := range ifaces {
			// setup dns search path
			domain, _ := getSearchDomains(iface)
			setSearchDomains(iface, ".")
			// restore dns search path
			defer setSearchDomains(iface, domain)
		}
	}

	rlimit()

	kubeWatch()
	dnsMain()

	ln, err := net.Listen("tcp", ":1234")
	if err != nil {
		log.Println(err)
		return
	}

	translator.Enable()
	translator.ForwardUDP(*dnsIP, "1233")
	defer translator.Disable()

	apply := tpu.Keepalive(1, `
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
`, "kubectl", "--kubeconfig", *kubeconfig, "apply", "-f", "-")
	apply.Wait()

	pf := tpu.Keepalive(0, "", "kubectl", "--kubeconfig", *kubeconfig, "port-forward", "pod/teleproxy", "8022")
        defer pf.Shutdown()
	// XXX: probably need some kind of keepalive check for ssh, first
	// curl after wakeup seems to trigger detection of death
//	ssh := tpu.Keepalive(0, "", "ssh", "-D", "localhost:1080", "-L", "localhost:9050:localhost:9050", "-C", "-N", "-oConnectTimeout=5", "-oExitOnForwardFailure=yes",
	ssh := tpu.Keepalive(0, "", "ssh", "-D", "localhost:1080", "-C", "-N", "-oConnectTimeout=5", "-oExitOnForwardFailure=yes",
		"-oStrictHostKeyChecking=no", "-oUserKnownHostsFile=/dev/null", "telepresence@localhost", "-p", "8022")
	defer ssh.Shutdown()

	limit := 10000
	log.Printf("Listening (limit %v)...", limit)
	go func() {
		sem := tpu.NewSemaphore(limit)
		for {
			conn, err := ln.Accept();
			if err != nil {
				log.Println(err)
			} else {
				switch conn.(type) {
				case *net.TCPConn:
					log.Println("AVAILABLE:", len(sem))
					sem.Acquire()
					go handleConnection(conn.(*net.TCPConn), sem)
				default:
					log.Println("Don't know how to handle conn:", conn)
				}
			}
		}
	}()

	ch := make(chan os.Signal)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	log.Println(<-ch)

	defer kickDNS()
}

func handleConnection(conn *net.TCPConn, sem tpu.Semaphore) {
	defer sem.Release()
	// hmm, we may not actually need to get the original destination,
	// we could just forward each ip to a unique port and either
	// listen on that port or run port-forward
	_, host, err := translator.GetOriginalDst(conn)
	if err != nil {
		log.Println("GetOriginalDst:", err)
		return
	}

	log.Println("CONNECT:", conn.RemoteAddr(), host)

	// setting up an ssh tunnel with dynamic socks proxy at this end
	// seems faster than connecting directly to a socks proxy
	dialer, err := proxy.SOCKS5("tcp", "localhost:1080", nil, proxy.Direct)
//	dialer, err := proxy.SOCKS5("tcp", "localhost:9050", nil, proxy.Direct)
	if err != nil {
		log.Println(err)
		conn.Close()
		return
	}

	_proxy, err := dialer.Dial("tcp", host)
	if err != nil {
		log.Println(err)
		conn.Close()
		return
	}
	proxy := _proxy.(*net.TCPConn)

	done := tpu.NewLatch(2)

	go pipe(conn, proxy, done)
	go pipe(proxy, conn, done)

	done.Wait()
}

func pipe(from, to *net.TCPConn, done tpu.Latch) {
	defer func() {
		log.Println("CLOSED WRITE:", to.RemoteAddr())
		to.CloseWrite()
	}()
	defer func() {
		log.Println("CLOSED READ:", from.RemoteAddr())
		from.CloseRead()
	}()
	defer done.Notify()

	const size = 64*1024
	var buf [size]byte
	for {
		n, err := from.Read(buf[0:size])
		if err != nil {
			if err != io.EOF {
				log.Println(err)
			}
			break
		} else {
			_, err := to.Write(buf[0:n])

			if err != nil {
				log.Println(err)
				break
			}
		}
	}
}

func getPIDs() (pids []int, err error) {
	cmd := exec.Command("ps", "-axo", "pid=,command=")
	out, err := cmd.CombinedOutput()
	if err != nil { return }
	if !cmd.ProcessState.Success() {
		err = fmt.Errorf("%s", out)
		return
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(strings.ToLower(line), "mdnsresponder") {
			parts := strings.Fields(line)
			var pid int
			pid, err = strconv.Atoi(parts[0])
			if err != nil { return }
			pids = append(pids, pid)
		}
	}

	return
}

func kickDNS() {
	pids, err := getPIDs()
	if err != nil {
		log.Println(err)
		return
	}

	log.Printf("Kicking DNS: %v\n", pids)
	for _, pid := range pids {
		proc, err := os.FindProcess(pid)
		if err != nil { log.Println(err) }
		err = proc.Signal(syscall.SIGHUP)
		if err != nil { log.Println(err) }
	}
}

func shell(command string) (result string, err error) {
	log.Println(command)
	cmd := exec.Command("sh", "-c", command)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Println(err)
		return
	}
	log.Printf("%s", out)
	result = string(out)
	return
}

func getIfaces() (ifaces []string, err error) {
	lines, err := shell("networksetup -listallnetworkservices | fgrep -v '*'")
	if err != nil { return }
	for _, line := range strings.Split(lines, "\n") {
		line = strings.TrimSpace(line)
		if len(line) > 0 {
			ifaces = append(ifaces, line)
		}
	}
	return
}

func getSearchDomains(iface string) (domains string, err error) {
	domains, err = shell(fmt.Sprintf("networksetup -getsearchdomains '%s'", iface))
	domains = strings.TrimSpace(domains)
	return
}

func setSearchDomains(iface, domains string) (err error) {
	_, err = shell(fmt.Sprintf("networksetup -setsearchdomains '%s' '%s'", iface, domains))
	return
}
