package daemon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	dns2 "github.com/miekg/dns"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/client/daemon/dns"
)

var errResolveDNotConfigured = errors.New("resolved not configured")

func (o *outbound) dnsServerWorker(c context.Context) error {
	if runningInDocker() {
		// Don't bother with systemd-resolved when running in a docker container
		return o.runOverridingServer(dgroup.WithGoroutineName(c, "/docker"))
	}

	err := o.tryResolveD(dgroup.WithGoroutineName(c, "/resolved"), o.router.dev)
	if err == errResolveDNotConfigured {
		err = nil
		if c.Err() == nil {
			dlog.Info(c, "Unable to use systemd-resolved, falling back to local server")
			err = o.runOverridingServer(dgroup.WithGoroutineName(c, "/legacy"))
		}
	}
	return err
}

// shouldApplySearch returns true if search path should be applied
func (o *outbound) shouldApplySearch(query string) bool {
	if len(o.search) == 0 {
		return false
	}

	if query == "localhost." {
		return false
	}

	// Don't apply search paths to the kubernetes zone
	if strings.HasSuffix(query, "."+o.router.clusterDomain) {
		return false
	}

	// Don't apply search paths if one is already there
	for _, s := range o.search {
		if strings.HasSuffix(query, s) {
			return false
		}
	}

	// Don't apply search path to namespaces or "svc".
	query = query[:len(query)-1]
	if lastDot := strings.LastIndexByte(query, '.'); lastDot >= 0 {
		tld := query[lastDot+1:]
		if _, ok := o.namespaces[tld]; ok || tld == "svc" {
			return false
		}
	}
	return true
}

// resolveInSearch is only used by the overriding resolver. It is needed because unlike other resolvers, this
// resolver does not hook into a DNS system that handles search paths prior to the arrival of the request.
//
// TODO: With the DNS lookups now being done in the cluster, there's only one reason left to have a search path,
// and that's the local-only intercepts which means that using search-paths really should be limited to that
// use-case.
func (o *outbound) resolveInSearch(c context.Context, qType uint16, query string) []net.IP {
	query = strings.ToLower(query)
	query = strings.TrimSuffix(query, tel2SubDomainDot)

	if !o.shouldDoClusterLookup(query) {
		return nil
	}

	if o.shouldApplySearch(query) {
		for _, s := range o.search {
			if ips := o.resolveInCluster(c, qType, query+s); len(ips) > 0 {
				return ips
			}
		}
	}
	return o.resolveInCluster(c, qType, query)
}

func (o *outbound) runOverridingServer(c context.Context) error {
	if o.dnsConfig.LocalIp == nil {
		dat, err := os.ReadFile("/etc/resolv.conf")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(dat), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "nameserver") {
				fields := strings.Fields(line)
				o.dnsConfig.LocalIp = net.ParseIP(fields[1])
				dlog.Infof(c, "Automatically set -dns=%s", net.IP(o.dnsConfig.LocalIp))
				break
			}
		}
	}
	if o.dnsConfig.LocalIp == nil {
		return errors.New("couldn't determine dns ip from /etc/resolv.conf")
	}

	listeners, err := o.dnsListeners(c)
	if err != nil {
		return err
	}
	dnsResolverAddr, err := splitToUDPAddr(listeners[0].LocalAddr())
	if err != nil {
		return err
	}
	dlog.Debugf(c, "Bootstrapping local DNS server on port %d", dnsResolverAddr.Port)

	// Create the connection later used for fallback. We need to create this before the firewall
	// rule because the rule must exclude the local address of this connection in order to
	// let it reach the original destination and not cause an endless loop.
	conn, err := dns2.Dial("udp", net.JoinHostPort(net.IP(o.dnsConfig.LocalIp).String(), "53"))
	sourcePort := conn.LocalAddr().(*net.UDPAddr)
	if err != nil {
		return err
	}
	defer func() {
		_ = conn.Close()
	}()

	serverStarted := make(chan struct{})
	serverDone := make(chan struct{})
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("Server", func(c context.Context) error {
		defer close(serverDone)
		select {
		case <-c.Done():
			return nil
		case <-o.router.configured():
			// Server will close the listener, so no need to close it here.
			o.processSearchPaths(g, func(c context.Context, paths []string) error {
				namespaces := make(map[string]struct{})
				search := make([]string, 0)
				for _, path := range paths {
					if strings.ContainsRune(path, '.') {
						search = append(search, path)
					} else if path != "" {
						namespaces[path] = struct{}{}
					}
				}
				o.domainsLock.Lock()
				o.namespaces = namespaces
				o.search = search
				o.domainsLock.Unlock()
				dns.Flush(c)
				return nil
			})
			v := dns.NewServer(c, listeners, conn, o.resolveInSearch)
			close(serverStarted)
			return v.Run(c)
		}
	})

	g.Go("NAT-redirect", func(c context.Context) error {
		select {
		case <-c.Done():
		case <-serverStarted:
			// Give DNS server time to start before rerouting NAT
			dtime.SleepWithContext(c, time.Millisecond)

			err := routeDNS(c, o.dnsConfig.LocalIp, dnsResolverAddr.Port, conn.LocalAddr().(*net.UDPAddr), sourcePort)
			if err != nil {
				return err
			}
			defer func() {
				c := context.Background()
				unrouteDNS(c)
				dns.Flush(c)
			}()
			dns.Flush(c)
			<-serverDone // Stay alive until DNS server is done
		}
		return nil
	})
	return g.Wait()
}

func (o *outbound) dnsListeners(c context.Context) ([]net.PacketConn, error) {
	listener, err := newLocalUDPListener(c)
	if err != nil {
		return nil, err
	}
	listeners := []net.PacketConn{listener}
	if runningInDocker() {
		// Inside docker. Don't add docker bridge
		return listeners, nil
	}

	// This is the default docker bridge. We need to listen here because the nat logic we use to intercept
	// dns packets will divert the packet to the interface it originates from, which in the case of
	// containers is the docker bridge. Without this dns won't work from inside containers.
	output, err := dexec.CommandContext(c, "docker", "inspect", "bridge",
		"-f", "{{(index .IPAM.Config 0).Gateway}}").Output()
	if err != nil {
		dlog.Info(c, "not listening on docker bridge")
		return listeners, nil
	}

	localAddr, err := splitToUDPAddr(listener.LocalAddr())
	if err != nil {
		return nil, err
	}

	dockerGatewayIP := net.ParseIP(strings.TrimSpace(string(output)))
	if dockerGatewayIP == nil || dockerGatewayIP.Equal(localAddr.IP) {
		return listeners, nil
	}

	// Check that the dockerGatewayIP is registered as an interface on this machine. When running WSL2 on
	// a Windows box, the gateway is managed by Windows and never visible to the Linux host and hence
	// will not be affected by the nat logic. Also, any attempt to listen to it will fail.
	found := false
	ifAddrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	for _, ifAddr := range ifAddrs {
		_, network, err := net.ParseCIDR(ifAddr.String())
		if err != nil {
			continue
		}
		if network.Contains(dockerGatewayIP) {
			found = true
			break
		}
	}

	if !found {
		dlog.Infof(c, "docker gateway %s is not visible as a network interface", dockerGatewayIP)
		return listeners, nil
	}

	for {
		extraAddr := &net.UDPAddr{IP: dockerGatewayIP, Port: localAddr.Port}
		ls, err := net.ListenPacket("udp", extraAddr.String())
		if err == nil {
			dlog.Infof(c, "listening to docker bridge at %s", dockerGatewayIP)
			return append(listeners, ls), nil
		}

		// the extraAddr was busy, try next available port
		for localAddr.Port++; localAddr.Port <= math.MaxUint16; localAddr.Port++ {
			if ls, err = net.ListenPacket("udp", localAddr.String()); err == nil {
				if localAddr, err = splitToUDPAddr(ls.LocalAddr()); err != nil {
					ls.Close()
					return nil, err
				}
				_ = listeners[0].Close()
				listeners = []net.PacketConn{ls}
				break
			}
		}
		if localAddr.Port > math.MaxUint16 {
			return nil, fmt.Errorf("unable to find a free port for both %s and %s", localAddr.IP, extraAddr.IP)
		}
	}
}

func runningInDocker() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

// runNatTableCmd runs "iptables -t nat ..."
func runNatTableCmd(c context.Context, args ...string) error {
	// We specifically don't want to use the cancellation of 'ctx' here, because we don't ever
	// want to leave things in a half-cleaned-up state.
	return dexec.CommandContext(c, "iptables", append([]string{"-t", "nat"}, args...)...).Run()
}

const tpDNSChain = "telepresence-dns"

// routeDNS creates a new chain in the "nat" table with two rules in it. One rule ensures
// that all packets sent to the currently configured DNS service are rerouted to our local
// DNS service. Another rule ensures that when our local DNS service cannot resolve and
// uses a fallback, that fallback reaches the original DNS service.
func routeDNS(c context.Context, dnsIP net.IP, toPort int, fallback *net.UDPAddr, sourcePort *net.UDPAddr) (err error) {
	// create the chain
	unrouteDNS(c)
	if err = runNatTableCmd(c, "-N", tpDNSChain); err != nil {
		return err
	}
	// Alter locally generated packets before routing
	if err = runNatTableCmd(c, "-I", "OUTPUT", "1", "-j", tpDNSChain); err != nil {
		return err
	}

	// This rule prevents that any rules in this table applies to the fallback address. I.e. we
	// let the fallback reach the original DNS service
	if err = runNatTableCmd(c, "-A", tpDNSChain,
		"-p", "udp",
		"--source", fallback.IP.String(),
		"--sport", strconv.Itoa(fallback.Port),
		"-j", "RETURN",
	); err != nil {
		return err
	}

	if err = runNatTableCmd(c, "-A", tpDNSChain,
		"-p", "udp",
		"--sport", strconv.Itoa(sourcePort.Port),
		"-j", "RETURN",
	); err != nil {
		return err
	}

	// This rule redirects all packets intended for the DNS service to our local DNS service
	return runNatTableCmd(c, "-A", tpDNSChain,
		"-p", "udp",
		"--dest", dnsIP.String()+"/32",
		"--dport", "53",
		"-j", "REDIRECT",
		"--to-ports", strconv.Itoa(toPort),
	)
}

// unrouteDNS removes the chain installed by routeDNS.
func unrouteDNS(c context.Context) {
	// The errors returned by these commands aren't of any interest besides logging. And they
	// are already logged since dexec is used.
	_ = runNatTableCmd(c, "-D", "OUTPUT", "-j", tpDNSChain)
	_ = runNatTableCmd(c, "-F", tpDNSChain)
	_ = runNatTableCmd(c, "-X", tpDNSChain)
}
