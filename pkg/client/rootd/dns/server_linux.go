package dns

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

const (
	maxRecursionTestRetries = 10

	// We use a fairly short delay here because if DNS recursion is a thing, then the cluster's DNS-server
	// has access to the caller host's network, so it runs locally in a Docker container or similar.
	recursionTestTimeout = 200 * time.Millisecond
)

var errResolveDNotConfigured = errors.New("resolved not configured")

func (s *Server) Worker(c context.Context, dev vif.Device, configureDNS func(net.IP, *net.UDPAddr)) error {
	if proc.RunningInContainer() {
		// Don't bother with systemd-resolved when running in a docker container
		return s.runOverridingServer(c, dev)
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

func (s *Server) runOverridingServer(c context.Context, dev vif.Device) error {
	if s.localIP == nil {
		rf, err := readResolveFile("/etc/resolv.conf")
		if err != nil {
			return err
		}
		dlog.Debug(c, rf.String())
		if len(rf.nameservers) > 0 {
			ip := iputil.Parse(rf.nameservers[0])
			s.localIP = ip
			dlog.Infof(c, "Automatically set -dns=%s", ip)
		}

		// The search entries in /etc/resolv.conf are not intended for this resolver so
		// ensure that we strip them off when we send queries to the cluster.
		for _, sp := range rf.search {
			lsp := len(sp)
			if lsp > 0 {
				if sp[0] == '.' {
					sp = sp[1:]
					lsp--
				}
				if lsp > 0 {
					if sp[lsp-1] != '.' {
						sp += "."
					}
					s.dropSuffixes = append(s.dropSuffixes, strings.ToLower(sp))
				}
			}
		}
	}
	if s.localIP == nil {
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

	// Create the connection pool later used for fallback. We need to create this before the firewall
	// rule because the rule must exclude the local address of this connection in order to
	// let it reach the original destination and not cause an endless loop.
	pool, err := NewConnPool(s.localIP.String(), 10)
	if err != nil {
		return err
	}
	defer func() {
		pool.Close()
	}()

	serverStarted := make(chan struct{})
	serverDone := make(chan struct{})
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("Server", func(c context.Context) error {
		defer close(serverDone)
		// Server will close the listener, so no need to close it here.
		s.processSearchPaths(g, func(c context.Context, _ vif.Device) error {
			s.flushDNS()
			return nil
		}, dev)
		return s.Run(c, serverStarted, listeners, pool, s.resolveInCluster)
	})

	if proc.RunningInContainer() {
		g.Go("Local DNS", func(c context.Context) error {
			select {
			case <-c.Done():
			case <-serverStarted:
				// Give DNS server time to start before rerouting NAT
				dtime.SleepWithContext(c, time.Millisecond)

				lc := net.ListenConfig{}
				pc, err := lc.ListenPacket(c, "udp", ":53")
				if err != nil {
					return nil
				}
				go func() {
					if err = forwarder.ForwardUDP(c, pc.(*net.UDPConn), dnsResolverAddr); err != nil {
						dlog.Error(c, err)
					}
				}()
			}
			return nil
		})
	}

	g.Go("NAT-redirect", func(c context.Context) error {
		select {
		case <-c.Done():
		case <-serverStarted:
			// Give DNS server time to start before rerouting NAT
			dtime.SleepWithContext(c, time.Millisecond)

			err := routeDNS(c, s.localIP, dnsResolverAddr, pool.LocalAddrs())
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
	if proc.RunningInContainer() {
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

// runNatTableCmd runs "iptables -t nat ...".
func runNatTableCmd(c context.Context, args ...string) error {
	// We specifically don't want to use the cancellation of 'ctx' here, because we don't ever
	// want to leave things in a half-cleaned-up state.
	args = append([]string{"-t", "nat"}, args...)
	cmd := dexec.CommandContext(c, "iptables", args...)
	cmd.DisableLogging = dlog.MaxLogLevel(c) < dlog.LogLevelDebug
	dlog.Debug(c, shellquote.ShellString("iptables", args))
	return cmd.Run()
}

const tpDNSChain = "TELEPRESENCE_DNS"

// routeDNS creates a new chain in the "nat" table with two rules in it. One rule ensures
// that all packets sent to the currently configured DNS service are rerouted to our local
// DNS service. Another rule ensures that when our local DNS service cannot resolve and
// uses a fallback, that fallback reaches the original DNS service.
func routeDNS(c context.Context, dnsIP net.IP, toAddr *net.UDPAddr, localDNSs []*net.UDPAddr) (err error) {
	// create the chain
	unrouteDNS(c)

	// Create the TELEPRESENCE_DNS chain
	if err = runNatTableCmd(c, "-N", tpDNSChain); err != nil {
		return err
	}

	// This rule prevents that any rules in this table applies to the localDNS address when
	// used as a source. I.e. we let the local DNS server reach the original DNS server
	for _, localDNS := range localDNSs {
		if err = runNatTableCmd(c, "-A", tpDNSChain,
			"-p", "udp",
			"--source", localDNS.IP.String(),
			"--sport", strconv.Itoa(localDNS.Port),
			"-j", "RETURN",
		); err != nil {
			return err
		}
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
