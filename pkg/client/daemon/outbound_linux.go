package daemon

import (
	"context"
	"fmt"
	"io/ioutil"
	"math"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/pkg/errors"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	rpc "github.com/datawire/telepresence2/rpc/v2/daemon"
	"github.com/datawire/telepresence2/v2/pkg/client/daemon/dns"
)

var errResolveDNotConfigured = errors.New("resolved not configured")

func (o *outbound) dnsServerWorker(c context.Context) error {
	err := o.tryResolveD(c)
	if err == errResolveDNotConfigured {
		dlog.Info(c, "Unable to use systemd-resolved, falling back to local server")
		err = o.runOverridingServer(c)
	}
	return err
}

// dnsResolverAddr obtains a new local address that the DNS resolver can listen to.
func dnsResolverAddr() (*net.UDPAddr, error) {
	l, err := net.ListenPacket("udp4", "localhost:")
	if err != nil {
		return nil, fmt.Errorf("unable to resolve udp addr: %v", err)
	}
	addr, ok := l.LocalAddr().(*net.UDPAddr)
	_ = l.Close()
	if !ok {
		// listening to udp should definitely return an *net.UDPAddr
		panic("cast error")
	}
	return addr, err
}

func (o *outbound) runOverridingServer(c context.Context) error {
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
		o.search = paths
	}

	listeners, err := dnsListeners(c)
	if err != nil {
		return err
	}
	o.dnsRedirPort = listeners[0].Port

	o.overridePrimaryDNS = true
	dgroup.ParentGroup(c).Go(proxyWorker, o.proxyWorker)

	srv := dns.NewServer(c, listeners, o.fallbackIP+":53", func(domain string) string {
		if r := o.resolve(domain); r != nil {
			return r.Ip
		}
		return ""
	})
	dlog.Debug(c, "Starting server")
	initDone := &sync.WaitGroup{}
	initDone.Add(1)
	err = srv.Run(c, initDone)
	dlog.Debug(c, "Server done")
	return err
}

// resolve looks up the given query in the (FIXME: somewhere), trying
// all the suffixes in the search path, and returns a Route on success
// or nil on failure. This implementation does not count the number of
// dots in the query.
func (o *outbound) resolve(query string) *rpc.Route {
	if !strings.HasSuffix(query, ".") {
		query += "."
	}

	var route *rpc.Route
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

func dnsListeners(c context.Context) ([]*net.UDPAddr, error) {
	localAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
	ls, err := net.ListenUDP("udp4", localAddr)
	if err != nil {
		return nil, err
	}
	localAddr = ls.LocalAddr().(*net.UDPAddr)
	ls.Close()

	listeners := []*net.UDPAddr{localAddr}
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
		ls, err = net.ListenUDP("udp4", extraAddr)
		if err == nil {
			ls.Close()
			dlog.Infof(c, "listening to docker bridge at %s", dockerGatewayIP)
			return append(listeners, extraAddr), nil
		}

		// the extraAddr was busy, try next available port
		for localAddr.Port++; localAddr.Port <= math.MaxUint16; localAddr.Port++ {
			if ls, err = net.ListenUDP("udp4", localAddr); err == nil {
				ls.Close()
				break
			}
		}
		if localAddr.Port > math.MaxUint16 {
			return nil, fmt.Errorf("unable to find a free port for both %s and %s", localAddr.IP, extraAddr.IP)
		}
	}
}
