package main

import (
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/user"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"strconv"
	"github.com/datawire/tp2/internal/pkg/tputil"
	"github.com/google/shlex"
	"github.com/miekg/dns"
	"golang.org/x/net/proxy"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/cache"
)

func ipt(argline string) {
	args, err := shlex.Split(argline)
	if err != nil { panic(err) }
	args = append([]string{"-t", "nat"}, args...)
	cmd := exec.Command("iptables", args...)
	log.Printf("iptables -t nat %s\n", argline)
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		log.Printf("%s", out)
	}
	if err != nil {
		log.Println(err)
	}
}

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

func removeRoute(key string) {
	old, ok := domainsToAddresses.Load(key)
	if ok {
		ipt("-D futz-1234 -j REDIRECT --dest " + old.(string) +
			"/32 -p tcp --to-ports 1234 -m ttl ! --ttl 42")
	}
}

func updateRoute(svc *v1.Service) {
	key := svc.Name + "."
	removeRoute(key)
	domainsToAddresses.Store(svc.Name + ".", svc.Spec.ClusterIP)
	ipt("-A futz-1234 -j REDIRECT --dest " + svc.Spec.ClusterIP +
		"/32 -p tcp --to-ports 1234 -m ttl ! --ttl 42")
}

type handler struct{}
func (this *handler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	switch r.Question[0].Qtype {
	case dns.TypeA:
		domain := r.Question[0].Name
		address, ok := domainsToAddresses.Load(domain)
		if ok {
			msg := dns.Msg{}
			msg.SetReply(r)
			msg.Authoritative = true
			msg.Answer = append(msg.Answer, &dns.A{
				Hdr: dns.RR_Header{ Name: domain, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60 },
				A: net.ParseIP(address.(string)),
			})
			w.WriteMsg(&msg)
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
	srv := &dns.Server{Addr: ":" + strconv.Itoa(1233), Net: "udp"}
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

	kubeWatch()
	go dnsMain()

	ln, err := net.Listen("tcp", ":1234")
	if err != nil {
		log.Println(err)
		return
	}

	// XXX: need to fix futz-1234 everywhere
	// XXX: -D only removes one copy of the rule, need to figure out how to remove all copies just in case
	ipt("-D OUTPUT -j futz-1234")
	// we need to be in the PREROUTING chain in order to get traffic
	// from docker containers, not sure you would *always* want this,
	// but probably makes sense as a default
	ipt("-D PREROUTING -j futz-1234")
	ipt("-N futz-1234")
	ipt("-F futz-1234")
	ipt("-I OUTPUT 1 -j futz-1234")
	ipt("-I PREROUTING 1 -j futz-1234")
	ipt("-A futz-1234 -j RETURN --dest 127.0.0.1/32 -p tcp")
	// XXX: need to figure out a better way to handle dns, maybe take an arg
	ipt("-A futz-1234 -j REDIRECT --dest " + *dnsIP + "/32 -p udp --to-ports 1233 -m ttl ! --ttl 42")

	cleanup := func () {
		// XXX: -D only removes one copy of the rule, need to figure out how to remove all copies just in case
		ipt("-D OUTPUT -j futz-1234")
		ipt("-D PREROUTING -j futz-1234")
		ipt("-F futz-1234")
		ipt("-X futz-1234")
	}

	defer cleanup()

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
	_, host, err := tputil.GetOriginalDst(conn)
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
