package daemon

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/pkg/errors"

	"github.com/datawire/telepresence2/pkg/client/daemon/dns"
	"github.com/datawire/telepresence2/pkg/client/daemon/proxy"
	route "github.com/datawire/telepresence2/pkg/rpc/iptables"
)

// worker names
const (
	CheckReadyWorker = "RDY"
	TranslatorWorker = "NAT"
	ProxyWorker      = "PXY"
	DNSServerWorker  = "DNS"
	DNSConfigWorker  = "CFG"
)

const (
	// DNSRedirPort is the port to which we redirect dns requests. It
	// should probably eventually be configurable and/or dynamically
	// chosen
	DNSRedirPort = "1233"

	// ProxyRedirPort is the port to which we redirect proxied IPs. It
	// should probably eventually be configurable and/or dynamically
	// chosen.
	ProxyRedirPort = "1234"
)

func dnsListeners(c context.Context, port string) (listeners []string) {
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
		output, err := dexec.CommandContext(c, "docker", "inspect", "bridge",
			"-f", "{{(index .IPAM.Config 0).Gateway}}").Output()
		if err != nil {
			dlog.Error(c, "not listening on docker bridge")
			return
		}
		extraIP := strings.TrimSpace(string(output))
		if extraIP != "127.0.0.1" && extraIP != "0.0.0.0" && extraIP != "" {
			listeners = append(listeners, fmt.Sprintf("%s:%s", extraIP, port))
		}
	}

	return
}

// start starts the interceptor, and only returns once the
// interceptor is successfully running in another goroutine.  It
// returns a function to call to shut down that goroutine.
//
// If dnsIP is empty, it will be detected from /etc/resolv.conf
//
// If fallbackIP is empty, it will default to Google DNS.
func start(c context.Context, dnsIP, fallbackIP string, noSearch bool) (*ipTables, context.CancelFunc, error) {
	if dnsIP == "" {
		dat, err := ioutil.ReadFile("/etc/resolv.conf")
		if err != nil {
			return nil, nil, err
		}
		for _, line := range strings.Split(string(dat), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "nameserver") {
				fields := strings.Fields(line)
				dnsIP = fields[1]
				dlog.Infof(c, "Automatically set -dns=%v", dnsIP)
				break
			}
		}
	}
	if dnsIP == "" {
		return nil, nil, errors.New("couldn't determine dns ip from /etc/resolv.conf")
	}

	if fallbackIP == "" {
		if dnsIP == "8.8.8.8" {
			fallbackIP = "8.8.4.4"
		} else {
			fallbackIP = "8.8.8.8"
		}
		dlog.Infof(c, "Automatically set -fallback=%v", fallbackIP)
	}
	if fallbackIP == dnsIP {
		return nil, nil, errors.New("if your fallbackIP and your dnsIP are the same, you will have a dns loop")
	}

	ic := newIPTables("traffic-manager")

	var shutdown context.CancelFunc
	c, shutdown = context.WithCancel(c)
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})

	ready := sync.WaitGroup{}
	ready.Add(2)

	g.Go(DNSServerWorker, func(c context.Context) error {
		srv := dns.NewServer(c, dnsListeners(c, DNSRedirPort), fallbackIP+":53", func(domain string) string {
			if r := ic.Resolve(domain); r != nil {
				return r.Ip
			}
			return ""
		})
		ready.Done()
		return srv.Run(c)
	})

	g.Go(ProxyWorker, func(c context.Context) error {
		// hmm, we may not actually need to get the original
		// destination, we could just forward each ip to a unique port
		// and either listen on that port or run port-forward
		pr, err := proxy.NewProxy(c, ":"+ProxyRedirPort, ic.destination)
		if err != nil {
			ready.Done()
			return errors.Wrap(err, "Proxy")
		}

		ready.Done()
		pr.Run(c, 10000)
		return nil
	})

	g.Go(TranslatorWorker, func(c context.Context) error {
		ready.Wait()
		return ic.run(c, DNSConfigWorker, func(c context.Context) error {
			bootstrap := route.Table{Name: "bootstrap", Routes: []*route.Route{{
				Ip:     dnsIP,
				Target: DNSRedirPort,
				Proto:  "udp",
			}}}
			ic.update(&bootstrap)
			dns.Flush()

			if noSearch {
				<-c.Done()
			} else {
				restore := dns.OverrideSearchDomains(c, ".")
				<-c.Done()
				restore()
			}
			dns.Flush()
			return nil
		})
	})
	return ic, shutdown, nil
}
