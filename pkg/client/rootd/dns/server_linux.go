package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strconv"
	"time"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

const (
	maxRecursionTestRetries = 10

	// We use a fairly short delay here because if DNS recursion is a thing, then the cluster's DNS-server
	// has access to the caller host's network, so it runs locally in a Docker container or similar.
	recursionTestTimeout = 200 * time.Millisecond
)

var errResolveDNotConfigured = errors.New("resolved not configured")

func (s *Server) Worker(c context.Context, dev vif.Device, configureDNS func(netip.AddrPort, netip.AddrPort)) error {
	if proc.RunningInContainer() {
		// Don't bother with systemd-resolved when running in a docker container
		return s.runContainerServer(c, dev, configureDNS)
	}

	err := s.tryResolveD(dgroup.WithGoroutineName(c, "/resolved"), dev, configureDNS)
	if err == errResolveDNotConfigured {
		err = nil
		if c.Err() == nil {
			dlog.Info(c, "Unable to use systemd-resolved, falling back to local server")
			err = s.runOverridingServer(dgroup.WithGoroutineName(c, "/legacy"), dev, configureDNS)
		}
	}
	return err
}

func addressFromResolvConf(c context.Context) (ap netip.AddrPort, err error) {
	var rf *dnsproxy.ResolveFile
	rf, err = dnsproxy.ReadResolveFile("/etc/resolv.conf")
	if err != nil {
		return ap, err
	}
	dlog.Debug(c, rf.String())
	if len(rf.Nameservers) > 0 {
		nsAddr := rf.Nameservers[0]
		addr, err := netip.ParseAddr(nsAddr)
		if err != nil {
			return ap, fmt.Errorf("nameserver IP %q in /etc/resolv.conf is invalid: %v", nsAddr, err)
		}
		p := rf.Port
		if p == 0 {
			p = 53
		}
		ap = netip.AddrPortFrom(addr, uint16(p))
	}
	return ap, nil
}

func (s *Server) runOverridingServer(c context.Context, dev vif.Device, configureDNS func(netip.AddrPort, netip.AddrPort)) error {
	if len(s.LocalAddresses) == 0 {
		ap, err := addressFromResolvConf(c)
		if err != nil {
			return err
		}
		s.LocalAddresses = []netip.AddrPort{ap}
		dlog.Infof(c, "Automatically set dns=%s", ap)
	}
	if len(s.LocalAddresses) == 0 {
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
	dlog.Debugf(c, "Bootstrapping local DNS server on port %d", dnsResolverAddr.Port())

	// Create the connection pool later used for fallback. We need to create this before the firewall
	// rule because the rule must exclude the local address of this connection in order to
	// let it reach the original destination and not cause an endless loop.
	pool, err := NewConnPool(s.LocalAddresses[0], 10)
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
		// The server will close the listener, so no need to close it here.
		s.processSearchPaths(g, func(c context.Context, _ vif.Device) error {
			s.flushDNS()
			return nil
		}, dev)
		return s.Run(c, serverStarted, listeners, pool)
	})

	configureDNS(s.VIFAddress, dnsResolverAddr)

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
					if err = forwarder.ForwardUDP(c, tunnel.ClientToDNS, pc.(*net.UDPConn), dnsResolverAddr); err != nil {
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

			err := routeDNS(c, s.LocalAddresses[0], dnsResolverAddr, pool.LocalAddrs())
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

func (s *Server) runContainerServer(c context.Context, dev vif.Device, configureDNS func(netip.AddrPort, netip.AddrPort)) error {
	lc := &net.ListenConfig{}
	l, err := lc.ListenPacket(c, "udp", ":53")
	if err != nil {
		return err
	}

	dnsResolverAddr, err := splitToUDPAddr(l.LocalAddr())
	if err != nil {
		return err
	}
	dlog.Debugf(c, "Bootstrapping local DNS server on port %d", dnsResolverAddr.Port())

	ap, err := addressFromResolvConf(c)
	if err != nil {
		return err
	}
	dlog.Debugf(c, "Using DNS fallback=%s", ap)
	pool, err := NewConnPool(ap, 10)
	if err != nil {
		return err
	}
	defer func() {
		pool.Close()
	}()

	dlog.Debugf(c, "Bootstrapping local DNS server on port %d", dnsResolverAddr.Port())
	serverStarted := make(chan struct{})
	serverDone := make(chan struct{})
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("Server", func(c context.Context) error {
		defer close(serverDone)
		// The server will close the listener, so no need to close it here.
		s.processSearchPaths(g, func(c context.Context, _ vif.Device) error {
			s.flushDNS()
			return nil
		}, dev)
		return s.Run(c, serverStarted, []net.PacketConn{l}, pool)
	})

	configureDNS(s.VIFAddress, dnsResolverAddr)
	return g.Wait()
}

func (s *Server) dnsListeners(c context.Context) ([]net.PacketConn, error) {
	listener, err := newLocalUDPListener(c)
	if err != nil {
		return nil, err
	}
	return []net.PacketConn{listener}, nil
}

// runNatTableCmd runs "iptables -t nat ...".
func runNatTableCmd(c context.Context, args ...string) error {
	// We specifically don't want to use the cancellation of 'ctx' here, because we don't ever
	// want to leave things in a half-cleaned-up state.
	c = context.WithoutCancel(c)
	args = append([]string{"-t", "nat"}, args...)
	cmd := exec.CommandContext(c, "iptables", args...)
	if dlog.MaxLogLevel(c) >= dlog.LogLevelTrace {
		dlog.Trace(c, shellquote.ShellString("iptables", args))
	}
	return cmd.Run()
}

const tpDNSChain = "TELEPRESENCE_DNS"

// routeDNS creates a new chain in the "nat" table with two rules in it. One rule ensures
// that all packets sent to the currently configured DNS service are rerouted to our local
// DNS service. Another rule ensures that when our local DNS service cannot resolve and
// uses a fallback, that fallback reaches the original DNS service.
func routeDNS(c context.Context, dnsAddress netip.AddrPort, toAddr netip.AddrPort, localDNSs []netip.AddrPort) (err error) {
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
			"--source", localDNS.Addr().String(),
			"--sport", strconv.Itoa(int(localDNS.Port())),
			"-j", "RETURN",
		); err != nil {
			return err
		}
	}
	// This rule redirects all packets intended for the DNS service to our local DNS service
	if err = runNatTableCmd(c, "-A", tpDNSChain,
		"-p", "udp",
		"--dest", dnsAddress.Addr().String()+"/32",
		"--dport", strconv.Itoa(int(dnsAddress.Port())),
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
	// The errors returned by these commands aren't of any interest besides logging.
	_ = runNatTableCmd(c, "-D", "OUTPUT", "-j", tpDNSChain)
	_ = runNatTableCmd(c, "-F", tpDNSChain)
	_ = runNatTableCmd(c, "-X", tpDNSChain)
}
