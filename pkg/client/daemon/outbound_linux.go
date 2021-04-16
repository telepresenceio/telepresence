package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
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
		dlog.Info(c, "Unable to use systemd-resolved, falling back to local server")
		err = o.runOverridingServer(dgroup.WithGoroutineName(c, "/legacy"))
	}
	return err
}

func (o *outbound) runOverridingServer(c context.Context) error {
	if o.dnsIP == nil {
		dat, err := ioutil.ReadFile("/etc/resolv.conf")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(dat), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "nameserver") {
				fields := strings.Fields(line)
				o.dnsIP = net.ParseIP(fields[1])
				dlog.Infof(c, "Automatically set -dns=%s", o.dnsIP)
				break
			}
		}
	}
	if o.dnsIP == nil {
		return errors.New("couldn't determine dns ip from /etc/resolv.conf")
	}

	o.setSearchPathFunc = func(c context.Context, paths []string) {
		namespaces := make(map[string]struct{})
		search := make([]string, 0)
		for _, path := range paths {
			if strings.ContainsRune(path, '.') {
				search = append(search, path)
			} else if path != "" {
				namespaces[path] = struct{}{}
			}
		}
		namespaces[tel2SubDomain] = struct{}{}
		o.domainsLock.Lock()
		o.namespaces = namespaces
		o.search = search
		o.domainsLock.Unlock()
	}

	listeners, err := o.dnsListeners(c)
	if err != nil {
		return err
	}
	dnsResolverAddr, err := splitToUDPAddr(listeners[0].LocalAddr())
	if err != nil {
		return err
	}
	ncc := withoutCancel{c}
	dlog.Debugf(c, "Bootstrapping local DNS server on port %d", dnsResolverAddr.Port)

	// Create the connection later used for fallback. We need to create this before the firewall
	// rule because the rule must exclude the local address of this connection in order to
	// let it reach the original destination and not cause an endless loop.
	conn, err := dns2.Dial("udp", o.dnsIP.String()+":53")
	if err != nil {
		return err
	}
	if err = routeDNS(ncc, o.dnsIP, dnsResolverAddr.Port, conn.LocalAddr().(*net.UDPAddr)); err != nil {
		return err
	}
	defer func() {
		_ = conn.Close()
		if err = unrouteDNS(ncc); err != nil {
			dlog.Error(c, err)
		}
	}()
	dns.Flush(c)
	srv := dns.NewServer(c, listeners, conn, o.resolveInCluster)
	close(o.dnsConfigured)
	dlog.Debug(c, "Starting server")
	err = srv.Run(c)
	dlog.Debug(c, "Server done")
	return err
}

func (o *outbound) dnsListeners(c context.Context) ([]net.PacketConn, error) {
	listeners := []net.PacketConn{o.dnsListener}
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

	localAddr, err := splitToUDPAddr(o.dnsListener.LocalAddr())
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

type withoutCancel struct {
	context.Context
}

func (withoutCancel) Deadline() (deadline time.Time, ok bool) { return }
func (withoutCancel) Done() <-chan struct{}                   { return nil }
func (withoutCancel) Err() error                              { return nil }
func (c withoutCancel) String() string                        { return fmt.Sprintf("%v.WithoutCancel", c.Context) }

// runNatTableCmd runs "iptables -t nat ..."
func runNatTableCmd(c context.Context, args ...string) error {
	// We specifically don't want to use the cancellation of 'ctx' here, because we don't ever
	// want to leave things in a half-cleaned-up state.
	return dexec.CommandContext(c, "iptables", append([]string{"-t", "nat"}, args...)...).Run()
}

const tpDNSChain = "telepresence-dns"

// routeDNS creates a new chain in the "nat" table with two rules in it. One rule ensures
// that all packages sent to the currently configured DNS service are rerouted to our local
// DNS service. Another rule ensures that when our local DNS service cannot resolve and
// uses a fallback, that fallback reaches the original DNS service.
func routeDNS(c context.Context, dnsIP net.IP, toPort int, fallback *net.UDPAddr) (err error) {
	// create the chain
	_ = unrouteDNS(c)
	if err = runNatTableCmd(c, "-N", tpDNSChain); err != nil {
		return err
	}
	// Alter locally generated packages before routing
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

	// This rule redirects all packages intended for the DNS service to our local DNS service
	return runNatTableCmd(c, "-A", tpDNSChain,
		"-p", "udp",
		"--dest", dnsIP.String()+"/32",
		"--dport", "53",
		"-j", "REDIRECT",
		"--to-ports", strconv.Itoa(toPort),
	)
}

// unrouteDNS removes the chain installed by routeDNS.
func unrouteDNS(c context.Context) (err error) {
	if err = runNatTableCmd(c, "-D", "OUTPUT", "-j", tpDNSChain); err != nil {
		return err
	}
	if err = runNatTableCmd(c, "-F", tpDNSChain); err != nil {
		return err
	}
	return runNatTableCmd(c, "-X", tpDNSChain)
}
