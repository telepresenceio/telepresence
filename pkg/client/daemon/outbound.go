package daemon

import (
	"fmt"
	"io/ioutil"
	"os"
	"runtime"
	"strings"

	"github.com/datawire/ambassador/pkg/supervisor"
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

// start starts the interceptor, and only returns once the
// interceptor is successfully running in another goroutine.  It
// returns a function to call to shut down that goroutine.
//
// If dnsIP is empty, it will be detected from /etc/resolv.conf
//
// If fallbackIP is empty, it will default to Google DNS.
func start(p *supervisor.Process, dnsIP, fallbackIP string, noSearch bool) (*ipTables, func(), error) {
	sup := p.Supervisor()

	if dnsIP == "" {
		dat, err := ioutil.ReadFile("/etc/resolv.conf")
		if err != nil {
			return nil, nil, err
		}
		for _, line := range strings.Split(string(dat), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "nameserver") {
				fields := strings.Fields(line)
				dnsIP = fields[1]
				p.Logf("Automatically set -dns=%v", dnsIP)
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
		p.Logf("Automatically set -fallback=%v", fallbackIP)
	}
	if fallbackIP == dnsIP {
		return nil, nil, errors.New("if your fallbackIP and your dnsIP are the same, you will have a dns loop")
	}

	ic := newIPTables("traffic-manager")
	sup.Supervise(&supervisor.Worker{
		Name: TranslatorWorker,
		// XXX: Requires will need to include the api server once it is changed to not bind early
		Requires: []string{ProxyWorker, DNSServerWorker},
		Work:     ic.run,
	})

	sup.Supervise(&supervisor.Worker{
		Name:     DNSServerWorker,
		Requires: []string{},
		Work: func(p *supervisor.Process) error {
			srv := dns.NewServer(p, dnsListeners(p, DNSRedirPort), fallbackIP+":53", func(domain string) string {
				if r := ic.Resolve(domain); r != nil {
					return r.Ip
				}
				return ""
			})
			err := srv.Start()
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
			pr, err := proxy.NewProxy(p, ":"+ProxyRedirPort, ic.destination)
			if err != nil {
				return errors.Wrap(err, "Proxy")
			}

			pr.Start(p, 10000)
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
			bootstrap := route.Table{Name: "bootstrap", Routes: []*route.Route{{
				Ip:     dnsIP,
				Target: DNSRedirPort,
				Proto:  "udp",
			}}}
			ic.update(&bootstrap)
			dns.Flush()

			if noSearch {
				p.Ready()
				<-p.Shutdown()
			} else {
				restore := dns.OverrideSearchDomains(p, ".")
				p.Ready()
				<-p.Shutdown()
				restore()
			}
			dns.Flush()
			return nil
		},
	})

	sup.Supervise(&supervisor.Worker{
		Name: CheckReadyWorker,
		Requires: []string{
			TranslatorWorker,
			DNSServerWorker,
			ProxyWorker,
			DNSConfigWorker},
		Work: func(p *supervisor.Process) error {
			<-p.Shutdown()
			return nil
		},
	})
	shutdown := func() {
		for _, n := range []string{CheckReadyWorker, DNSConfigWorker, ProxyWorker, DNSServerWorker, TranslatorWorker} {
			sup.Get(n).Shutdown()
		}
	}
	return ic, shutdown, nil
}
