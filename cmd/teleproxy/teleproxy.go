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
	"syscall"
	"github.com/datawire/teleproxy/internal/pkg/dns"
	"github.com/datawire/teleproxy/internal/pkg/interceptor"
	"github.com/datawire/teleproxy/internal/pkg/k8s"
	"github.com/datawire/teleproxy/internal/pkg/route"
	"github.com/datawire/teleproxy/internal/pkg/tpu"
	"golang.org/x/net/proxy"
	"k8s.io/api/core/v1"
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

	tpu.Rlimit()

	iceptor := interceptor.NewInterceptor("teleproxy")
	k8s.Watch(*kubeconfig, func(svcs []*v1.Service) {
		table := route.Table{Name: "kubernetes"}
		for _, svc := range svcs {
			if svc.Spec.ClusterIP == "None" { continue }
			table.Add(route.Route{
				Name: svc.Name,
				Ip: svc.Spec.ClusterIP,
				Proto: "tcp",
				Target: "1234",
			})
		}
		iceptor.Update(table)
		kickDNS()
	})

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

	ln, err := net.Listen("tcp", ":1234")
	if err != nil {
		log.Println(err)
		return
	}

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

	iceptor.Start()
	defer iceptor.Stop()
	iceptor.Update(bootstrap)

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
					go func() {
						defer sem.Release()
						handleConnection(iceptor, conn.(*net.TCPConn))
					}()
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

func handleConnection(iceptor *interceptor.Interceptor, conn *net.TCPConn) {
	// hmm, we may not actually need to get the original destination,
	// we could just forward each ip to a unique port and either
	// listen on that port or run port-forward
	host, err := iceptor.Destination(conn)
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
