package interceptor

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"git.lukeshu.com/go/libsystemd/sd_daemon"

	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/datawire/telepresence2/pkg/dns"
	"github.com/datawire/telepresence2/pkg/proxy"
	"github.com/datawire/telepresence2/pkg/route"
	"github.com/pkg/errors"
)

// worker names
const (
	CheckReadyWorker = "RDY"
	TranslatorWorker = "NAT"
	ProxyWorker      = "PXY"
	APIWorker        = "API"
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

func selfCheck(p *supervisor.Process) error {
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

// Start starts the interceptor, and only returns once the
// interceptor is successfully running in another goroutine.  It
// returns a function to call to shut down that goroutine.
//
// If dnsIP is empty, it will be detected from /etc/resolv.conf
//
// If fallbackIP is empty, it will default to Google DNS.
func Start(p *supervisor.Process, dnsIP, fallbackIP string, noCheck, noSearch bool) error {
	if os.Geteuid() != 0 {
		return errors.New("ERROR: teleproxy must be run as root or suid root")
	}

	sup := p.Supervisor()

	if dnsIP == "" {
		dat, err := ioutil.ReadFile("/etc/resolv.conf")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(dat), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "nameserver") {
				fields := strings.Fields(line)
				dnsIP = fields[1]
				// log.Printf("TPY: Automatically set -dns=%v", dnsIP)
				break
			}
		}
	}
	if dnsIP == "" {
		return errors.New("couldn't determine dns ip from /etc/resolv.conf")
	}

	if fallbackIP == "" {
		if dnsIP == "8.8.8.8" {
			fallbackIP = "8.8.4.4"
		} else {
			fallbackIP = "8.8.8.8"
		}
		// log.Printf("TPY: Automatically set -fallback=%v", fallbackIP)
	}
	if fallbackIP == dnsIP {
		return errors.New("if your fallbackIP and your dnsIP are the same, you will have a dns loop")
	}

	ic := newInterceptor("teleproxy")
	apis, err := ic.newAPIServer()
	if err != nil {
		return errors.Wrap(err, "API Server")
	}

	sup.Supervise(&supervisor.Worker{
		Name: TranslatorWorker,
		// XXX: Requires will need to include the api server once it is changed to not bind early
		Requires: []string{ProxyWorker, DNSServerWorker},
		Work:     ic.Work,
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
				Fallback:  fallbackIP + ":53",
				Resolve: func(domain string) string {
					if r := ic.Resolve(domain); r != nil {
						return r.Ip
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
			pr, err := proxy.NewProxy(fmt.Sprintf(":%s", ProxyRedirPort), ic.Destination)
			if err != nil {
				return errors.Wrap(err, "Proxy")
			}

			pr.Start(10000)
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
				Ip:     dnsIP,
				Target: DNSRedirPort,
				Proto:  "udp",
			})
			bootstrap.Add(route.Route{
				Name:   "teleproxy",
				Ip:     MagicIP,
				Target: apis.Port(),
				Proto:  "tcp",
			})
			ic.Update(bootstrap)

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
			APIWorker,
			DNSServerWorker,
			ProxyWorker,
			DNSConfigWorker},
		Work: func(p *supervisor.Process) error {
			err := selfCheck(p)
			if err != nil {
				if noCheck {
					p.Logf("WARNING, SELF CHECK FAILED: %v", err)
				} else {
					return errors.Wrap(err, "SELF CHECK FAILED")
				}
			} else {
				// p.Logf("SELF CHECK PASSED, SIGNALING READY")
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

	return nil
}
