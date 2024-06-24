package dns

import (
	"context"
	"fmt"
	"net"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

const (
	maxRecursionTestRetries = 40
	recursionTestTimeout    = 1500 * time.Millisecond
)

func (s *Server) Worker(c context.Context, dev vif.Device, configureDNS func(net.IP, *net.UDPAddr)) error {
	listener, err := newLocalUDPListener(c)
	if err != nil {
		return err
	}
	dnsAddr, err := splitToUDPAddr(listener.LocalAddr())
	if err != nil {
		return err
	}
	configureDNS(s.remoteIP, dnsAddr)

	var pool FallbackPool
	if client.GetConfig(c).OSSpecific().Network.DNSWithFallback {
		// Create the connection pool later used for fallback.
		dnsServers, err := getDNSServerList()
		if err != nil {
			dlog.Warnf(c, "Failed to get DNS servers: %v", err)
		} else {
			for _, dnsServer := range dnsServers {
				p, err := NewConnPool(dnsServer, 10)
				if err == nil {
					dlog.Infof(c, "Using fallback DNS server: %s", dnsServer)
					pool = p
					break
				}
				dlog.Warn(c, err)
			}
			if pool == nil {
				dlog.Warnf(c, "No viable fallback DNS server found")
			} else {
				defer pool.Close()
			}
		}
	}

	// Start local DNS server
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	g.Go("Server", func(c context.Context) error {
		// No need to close listener. It's closed by the dns server.
		defer func() {
			c, cancel := context.WithTimeout(context.WithoutCancel(c), 5*time.Second)
			s.Lock()
			_ = dev.SetDNS(c, s.clusterDomain, s.remoteIP, nil)
			s.Unlock()
			cancel()
		}()
		if err := s.updateRouterDNS(c, dev); err != nil {
			return err
		}
		s.processSearchPaths(g, s.updateRouterDNS, dev)
		return s.Run(c, make(chan struct{}), []net.PacketConn{listener}, pool, s.resolveInCluster)
	})
	return g.Wait()
}

func (s *Server) updateRouterDNS(c context.Context, dev vif.Device) error {
	s.Lock()
	err := dev.SetDNS(c, s.clusterDomain, s.remoteIP, s.search)
	s.Unlock()
	s.flushDNS()
	if err != nil {
		return fmt.Errorf("failed to set DNS: %w", err)
	}
	return nil
}

func getDNSServerList() ([]string, error) {
	iphlpapi := windows.NewLazyDLL("iphlpapi.dll")
	getNetworkParams := iphlpapi.NewProc("GetNetworkParams")

	// First, call GetNetworkParams with a nil buffer to get the required buffer size
	var bufferSize uint32
	ret, _, _ := getNetworkParams.Call(uintptr(unsafe.Pointer(nil)), uintptr(unsafe.Pointer(&bufferSize)))
	if ret != uintptr(windows.ERROR_BUFFER_OVERFLOW) {
		return nil, windows.Errno(ret)
	}

	// Allocate the required buffer size
	buffer := make([]byte, bufferSize)

	// Call GetNetworkParams with the allocated buffer
	ret, _, _ = getNetworkParams.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&bufferSize)))
	if ret != 0 {
		return nil, windows.Errno(ret)
	}

	// Define the FIXED_INFO structure
	type FIXED_INFO struct {
		HostName         [132]byte
		DomainName       [132]byte
		CurrentDNSServer *windows.IpAddrString
		DNSServerList    windows.IpAddrString
		NodeType         uint32
		ScopeID          [260]byte
		EnableRouting    uint32
		EnableProxy      uint32
		EnableDNS        uint32
	}

	// Convert buffer to FIXED_INFO structure
	fi := (*FIXED_INFO)(unsafe.Pointer(&buffer[0]))

	// Traverse the DNS server list
	sl := fi.DNSServerList
	var svcs []string
	for {
		svcs = append(svcs, windows.BytePtrToString(&sl.IpAddress.String[0]))
		if sl.Next == nil {
			break
		}
		sl = *sl.Next
	}
	return svcs, nil
}
