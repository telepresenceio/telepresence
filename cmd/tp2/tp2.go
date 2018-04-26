package main

import (
	"flag"
	"fmt"
	"io"
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
	"github.com/datawire/tp2/internal/pkg/nat"
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
// XXX: need to do better than tp2
var translator = nat.NewTranslator("tp2")

func removeRoute(key string) {
	if old, ok := domainsToAddresses.Load(key); ok {
		translator.ClearTCP(old.(string))
	}
}

func updateRoute(svc *v1.Service) {
	if svc.Spec.ClusterIP == "" { return }
	domainsToAddresses.Store(svc.Name + ".", svc.Spec.ClusterIP)
	translator.ForwardTCP(svc.Spec.ClusterIP, "1234")
	kickDNS()
}

type handler struct{}
func (this *handler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	log.Println(r.Question[0].Qtype, "DNS request for", r.Question[0].Name)
	switch r.Question[0].Qtype {
	case dns.TypeA:
		domain := r.Question[0].Name
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
		domain := r.Question[0].Name
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
	// turns out you need to listen on localhost for nat to work
	// properly for udp, otherwise you get an "unexpected source
	// blah thingy" because the dns reply packets look like they
	// are coming from the wrong place
	srv := &dns.Server{Addr: "127.0.0.1:" + strconv.Itoa(1233), Net: "udp"}
	srv.Handler = &handler{}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Failed to set udp listener %s\n", err.Error())
	}
}

var dnsIP = flag.String("dns", "10.0.0.1", "dns ip address")
var fallbackIP = flag.String("fallback", "", "dns fallback")
var remote = flag.String("remote", "", "remote host")

func main() {
	flag.Parse()

	if *kubeconfig == "" {
		current, err := user.Current()
		if err != nil { panic(err) }
		home := current.HomeDir
		*kubeconfig = filepath.Join(home, ".kube/config")
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
		// setup dns search path
		iface, _ := getIface()
		domains, _ := getSearchDomains(iface)
		setSearchDomains(iface, ".")
		// restore dns search path
		defer setSearchDomains(iface, domains)
	}

	kubeWatch()
	go dnsMain()

	ln, err := net.Listen("tcp", ":1234")
	if err != nil {
		log.Println(err)
		return
	}

	translator.Enable()
	translator.ForwardUDP(*dnsIP, "1233")
	defer translator.Disable()

	sshch := make(chan bool)
	defer func() { sshch<-true }()

	// XXX: probably need some kind of keepalive check for ssh, first
	// curl after wakeup seems to trigger detection of death
	go func() {
		OUTER:
		for {
			ssh := exec.Command("ssh", "-D", "localhost:1080", "-C", "-N", "-oExitOnForwardFailure=yes",
				"-oStrictHostKeyChecking=no", "telepresence@" + *remote)

			pipe, err := ssh.StderrPipe()
			if err != nil { panic(err) }
			go reader(pipe)

			pipe, err = ssh.StdoutPipe()
			if err != nil { panic(err) }
			go reader(pipe)

			log.Println(strings.Join(ssh.Args, " "))
			err = ssh.Start()
			if err != nil { panic(err) }

			exitch := make(chan bool)

			go func() {
				err = ssh.Wait()
				if err != nil {
					log.Println(err)
				}
				exitch<-true
			}()

			select {
			case <-sshch:
				log.Println("Killing ssh...")
				err = ssh.Process.Kill()
				if err != nil {
					log.Println(err)
				}
				break OUTER
			case <-exitch:
				log.Println("Waiting 1 second before restarting ssh...")
				time.Sleep(time.Second)
				continue OUTER
			}
		}
	}()

	log.Println("Listening...")
	go func() {
		for {
			conn, err := ln.Accept();
			if err != nil {
				log.Println(err)
			} else {
				switch conn.(type) {
				case *net.TCPConn:
					go handleConnection(conn.(*net.TCPConn))
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

func reader(pipe io.ReadCloser) {
	const size = 64*1024
	var buf [size]byte
	for {
		n, err := pipe.Read(buf[:size])
		if err != nil {
			pipe.Close()
			return
		}
		log.Printf("%s", buf[:n])
	}
}

func handleConnection(conn *net.TCPConn) {
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

	go pipe(conn, proxy)
	go pipe(proxy, conn)
}

func pipe(from, to *net.TCPConn) {
	defer func() {
		log.Println("CLOSED WRITE:", to.RemoteAddr())
		to.CloseWrite()
	}()
	defer func() {
		log.Println("CLOSED READ:", from.RemoteAddr())
		from.CloseRead()
	}()

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

func getIface() (iface string, err error) {
	iface, err = shell("networksetup -listnetworkserviceorder | head -2 | tail -1 | cut -f2-100 -d' '")
	iface = strings.TrimSpace(iface)
	return
}

func getSearchDomains(iface string) (domains string, err error) {
	domains, err = shell(fmt.Sprintf("networksetp -getsearchdomains '%s'", iface))
	domains = strings.TrimSpace(domains)
	return
}

func setSearchDomains(iface, domains string) (err error) {
	_, err = shell(fmt.Sprintf("networksetup -setsearchdomains '%s' '%s'", iface, domains))
	return
}
