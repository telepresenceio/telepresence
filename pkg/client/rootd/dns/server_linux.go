package dns

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
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

var errResolveDNotConfigured = errors.New("resolved not configured")

func (s *Server) Worker(c context.Context, dev *vif.Device, configureDNS func(net.IP, *net.UDPAddr)) error {
	if runningInDocker() {
		// Don't bother with systemd-resolved when running in a docker container
		return s.runOverridingServer(dgroup.WithGoroutineName(c, "/docker"), dev)
	}

	err := s.tryResolveD(dgroup.WithGoroutineName(c, "/resolved"), dev, configureDNS)
	if err == errResolveDNotConfigured {
		err = nil
		if c.Err() == nil {
			dlog.Info(c, "Unable to use systemd-resolved, falling back to local server")
			err = s.runOverridingServer(dgroup.WithGoroutineName(c, "/legacy"), dev)
		}
	}
	return err
}

// shouldApplySearch returns true if search path should be applied
func (s *Server) shouldApplySearch(query string) bool {
	if len(s.search) == 0 {
		return false
	}

	if query == "localhost." {
		return false
	}

	// Don't apply search paths to the kubernetes zone
	if strings.HasSuffix(query, "."+s.clusterDomain) {
		return false
	}

	// Don't apply search paths if one is already there
	for _, s := range s.search {
		if strings.HasSuffix(query, s) {
			return false
		}
	}

	// Don't apply search path to namespaces or "svc".
	query = query[:len(query)-1]
	if lastDot := strings.LastIndexByte(query, '.'); lastDot >= 0 {
		tld := query[lastDot+1:]
		if _, ok := s.namespaces[tld]; ok || tld == "svc" {
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
func (s *Server) resolveInSearch(c context.Context, query string) ([]net.IP, error) {
	query = strings.ToLower(query)
	query = strings.TrimSuffix(query, tel2SubDomainDot)

	if !s.shouldDoClusterLookup(query) {
		return nil, nil
	}

	if s.shouldApplySearch(query) {
		for _, sp := range s.search {
			if ips, err := s.resolveInCluster(c, query+sp); err != nil || len(ips) > 0 {
				return ips, err
			}
		}
	}
	return s.resolveInCluster(c, query)
}

func (s *Server) runOverridingServer(c context.Context, dev *vif.Device) error {
	if s.config.LocalIp == nil {
		dat, err := os.ReadFile("/etc/resolv.conf")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(dat), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "nameserver") {
				fields := strings.Fields(line)
				ip := net.ParseIP(fields[1])
				if ip.To4() != nil {
					s.config.LocalIp = ip.To4()
					dlog.Infof(c, "Automatically set -dns=%s", net.IP(s.config.LocalIp))
				}
			}

			// The search entry in /etc/resolv.conf is not intended for this resolver so
			// ensure that we just forward such queries without sending them to the cluster
			// by adding corresponding entries to excludeSuffixes
			if strings.HasPrefix(strings.TrimSpace(line), "search") {
				fields := strings.Fields(line)
				for _, field := range fields[1:] {
					s.config.ExcludeSuffixes = append(s.config.ExcludeSuffixes, "."+field)
				}
			}
		}
	}
	if s.config.LocalIp == nil {
		return errors.New("couldn't determine dns ip from /etc/resolv.conf")
	}

	listeners, err := s.dnsListeners(c)
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
	conn, err := dns2.Dial("udp", net.JoinHostPort(net.IP(s.config.LocalIp).String(), "53"))
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
		// Server will close the listener, so no need to close it here.
		s.processSearchPaths(g, func(c context.Context, paths []string, _ *vif.Device) error {
			namespaces := make(map[string]struct{})
			search := make([]string, 0)
			for _, path := range paths {
				if strings.ContainsRune(path, '.') {
					search = append(search, path)
				} else if path != "" {
					namespaces[path] = struct{}{}
				}
			}
			s.domainsLock.Lock()
			s.namespaces = namespaces
			s.search = search
			s.domainsLock.Unlock()
			s.flushDNS()
			return nil
		}, dev)
		return s.Run(c, serverStarted, listeners, conn, s.resolveInSearch)
	})

	g.Go("NAT-redirect", func(c context.Context) error {
		select {
		case <-c.Done():
		case <-serverStarted:
			// Give DNS server time to start before rerouting NAT
			dtime.SleepWithContext(c, time.Millisecond)

			err := routeDNS(c, s.config.LocalIp, dnsResolverAddr, conn.LocalAddr().(*net.UDPAddr))
			if err != nil {
				return err
			}
			defer func() {
				c := context.Background()
				unrouteDNS(c)
				s.flushDNS()
			}()
			s.flushDNS()
			<-serverDone // Stay alive until DNS server is done
		}
		return nil
	})
	return g.Wait()
}

func (s *Server) dnsListeners(c context.Context) ([]net.PacketConn, error) {
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

const tpDNSChain = "TELEPRESENCE_DNS"

// routeDNS creates a new chain in the "nat" table with two rules in it. One rule ensures
// that all packets sent to the currently configured DNS service are rerouted to our local
// DNS service. Another rule ensures that when our local DNS service cannot resolve and
// uses a fallback, that fallback reaches the original DNS service.
func routeDNS(c context.Context, dnsIP net.IP, toAddr *net.UDPAddr, localDNS *net.UDPAddr) (err error) {
	// create the chain
	unrouteDNS(c)

	// Create the TELEPRESENCE_DNS chain
	if err = runNatTableCmd(c, "-N", tpDNSChain); err != nil {
		return err
	}

	// This rule prevents that any rules in this table applies to the localDNS address when
	// used as a source. I.e. we let the local DNS server reach the original DNS server
	if err = runNatTableCmd(c, "-A", tpDNSChain,
		"-p", "udp",
		"--source", localDNS.IP.String(),
		"--sport", strconv.Itoa(localDNS.Port),
		"-j", "RETURN",
	); err != nil {
		return err
	}

	// This rule redirects all packets intended for the DNS service to our local DNS service
	if err = runNatTableCmd(c, "-A", tpDNSChain,
		"-p", "udp",
		"--dest", dnsIP.String()+"/32",
		"--dport", "53",
		"-j", "DNAT",
		"--to-destination", toAddr.String(),
	); err != nil {
		return err
	}

	// Alter locally generated packets before routing
	return runNatTableCmd(c, "-I", "OUTPUT", "1", "-j", tpDNSChain)
}

// unrouteDNS removes the chain installed by routeDNS.
func unrouteDNS(c context.Context) {
	// The errors returned by these commands aren't of any interest besides logging. And they
	// are already logged since dexec is used.
	_ = runNatTableCmd(c, "-D", "OUTPUT", "-j", tpDNSChain)
	_ = runNatTableCmd(c, "-F", tpDNSChain)
	_ = runNatTableCmd(c, "-X", tpDNSChain)
}
