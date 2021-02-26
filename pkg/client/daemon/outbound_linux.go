package daemon

import (
	"context"
	"fmt"
	"io/ioutil"
	"math"
	"net"
	"os"
	"strings"

	"github.com/pkg/errors"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/telepresence2/v2/pkg/client/daemon/dns"
	"github.com/datawire/telepresence2/v2/pkg/client/daemon/nat"
)

var errResolveDNotConfigured = errors.New("resolved not configured")

func (o *outbound) dnsServerWorker(c context.Context, onReady func()) error {
	err := o.tryResolveD(dgroup.WithGoroutineName(c, "/resolved"), onReady)
	if err == errResolveDNotConfigured {
		dlog.Info(c, "Unable to use systemd-resolved, falling back to local server")
		err = o.runOverridingServer(dgroup.WithGoroutineName(c, "/legacy"), onReady)
	}
	return err
}

func (o *outbound) runOverridingServer(c context.Context, onReady func()) error {
	if o.dnsIP == "" {
		dat, err := ioutil.ReadFile("/etc/resolv.conf")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(dat), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "nameserver") {
				fields := strings.Fields(line)
				o.dnsIP = fields[1]
				dlog.Infof(c, "Automatically set -dns=%v", o.dnsIP)
				break
			}
		}
	}
	if o.dnsIP == "" {
		return errors.New("couldn't determine dns ip from /etc/resolv.conf")
	}

	if o.fallbackIP == "" {
		if o.dnsIP == "8.8.8.8" {
			o.fallbackIP = "8.8.4.4"
		} else {
			o.fallbackIP = "8.8.8.8"
		}
		dlog.Infof(c, "Automatically set -fallback=%v", o.fallbackIP)
	}
	if o.fallbackIP == o.dnsIP {
		return errors.New("if your fallbackIP and your dnsIP are the same, you will have a dns loop")
	}

	o.setSearchPathFunc = func(c context.Context, paths []string) {
		paths = append(paths, "svc.cluster.local.", "cluster.local.", "")
		o.search = paths
	}

	listeners, err := o.dnsListeners(c)
	if err != nil {
		return err
	}
	dnsAddr, err := splitToUDPAddr(listeners[0].LocalAddr())
	if err != nil {
		return err
	}
	o.dnsRedirPort = dnsAddr.Port

	o.overridePrimaryDNS = true
	onReady()

	srv := dns.NewServer(c, listeners, o.fallbackIP+":53", func(domain string) string {
		if r := o.resolve(domain); r != nil {
			return o.getIP(r.Ips)
		}
		return ""
	})
	dlog.Debug(c, "Starting server")
	err = srv.Run(c)
	dlog.Debug(c, "Server done")
	return err
}

// resolve looks up the given query in the (FIXME: somewhere), trying
// all the suffixes in the search path, and returns a Route on success
// or nil on failure. This implementation does not count the number of
// dots in the query.
func (o *outbound) resolve(query string) *nat.Route {
	if !strings.HasSuffix(query, ".") {
		query += "."
	}

	var route *nat.Route
	o.searchLock.RLock()
	o.domainsLock.RLock()
	for _, suffix := range o.search {
		name := query + suffix
		if route = o.domains[strings.ToLower(name)]; route != nil {
			break
		}
	}
	o.searchLock.RUnlock()
	o.domainsLock.RUnlock()
	return route
}

func (o *outbound) dnsListeners(c context.Context) ([]net.PacketConn, error) {
	listeners := []net.PacketConn{o.dnsListener}
	if _, err := os.Stat("/.dockerenv"); err == nil {
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
