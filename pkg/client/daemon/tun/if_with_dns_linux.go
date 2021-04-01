package tun

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/ipv4"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/daemon/dbus"
	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
	"github.com/telepresenceio/telepresence/v2/pkg/tun"
)

const (
	// I use TUN interface, so only plain IP packet, no ethernet header + mtu is set to 1300
	bufSize      = 1500
	mtu          = 1300
	udpProto     = 0x11
	udpHeaderLen = 8
)

// An InterfaceWithDNS is a TUN device capable of dispatching DNS requests that are sent to
// its configured DNS to a DNS server that uses a local address.
type InterfaceWithDNS struct {
	*tun.Device
	requestCount  uint64
	responderPool *sync.Pool
	ifIP          net.IP
	dnsIP         net.IP
}

// responder associates a local UDP connection with the source port of requests that
// will respond to.
type responder struct {
	tun  *InterfaceWithDNS
	conn *net.UDPConn
	buf  []byte
	raw  []byte
	port uint16
}

// The UDP datagram and its payload
type udpDatagram []byte

func (u udpDatagram) setValues(src, dst uint16, body []byte) {
	binary.BigEndian.PutUint16(u, src)
	binary.BigEndian.PutUint16(u[2:], dst)
	binary.BigEndian.PutUint16(u[4:], uint16(udpHeaderLen+len(body)))
	copy(u[udpHeaderLen:], body)
}

func (u udpDatagram) source() uint16 {
	return binary.BigEndian.Uint16(u)
}

func (u udpDatagram) destination() uint16 {
	return binary.BigEndian.Uint16(u[2:])
}

func (u udpDatagram) length() uint16 {
	return binary.BigEndian.Uint16(u[4:])
}

func (u udpDatagram) checksum() uint16 {
	return binary.BigEndian.Uint16(u[6:])
}

func (u udpDatagram) setChecksum(src, dst net.IP, proto byte) {
	// reset current checksum, if any
	binary.BigEndian.PutUint16(u[6:], 0)

	buf := make([]byte, 12+len(u))

	// Write a pseudo-header with src IP, dst IP, 0, protocol, and datagram length
	copy(buf, src)
	copy(buf[4:], dst)
	buf[8] = 0
	buf[9] = proto
	binary.BigEndian.PutUint16(buf[10:], u.length())
	copy(buf[12:], u)

	// compute and assign checksum
	binary.BigEndian.PutUint16(u[6:], checksum(buf))
}

func (u udpDatagram) body() []byte {
	return u[udpHeaderLen:]
}

func (u udpDatagram) String() string {
	return fmt.Sprintf("src=%d, dst=%d, length=%d, crc=%d", u.source(), u.destination(), u.length(), u.checksum())
}

func checksum(buf []byte) uint16 {
	s := uint32(0)
	t := len(buf)
	if (t % 2) != 0 {
		// uneven length, add last byte << 8
		t--
		s = uint32(buf[t]) << 8
	}
	for i := 0; i < t; i += 2 {
		s += uint32(buf[i])<<8 | uint32(buf[i+1])
	}
	for s > 0xffff {
		s = (s >> 16) + (s & 0xffff)
	}
	c := ^uint16(s)

	if c == 0 {
		// From RFC 768: If the computed checksum is zero, it is transmitted as all ones.
		c = 0xffff
	}
	return c
}

// CreateInterfaceWithDNS creates a new TUN device and assigns the first available class C subnet
// to it. The x.x.x.2 address of that network is then declared to be the DNS service of that
// network. The interface is then set to state "up".
func CreateInterfaceWithDNS(c context.Context, dConn *dbus.ResolveD) (*InterfaceWithDNS, error) {
	// Obtain an available class C subnet
	ifCIDR, err := subnet.FindAvailableClassC()
	if err != nil {
		return nil, err
	}

	dev, err := tun.OpenTun()
	if err != nil {
		return nil, fmt.Errorf("failed to open virtual network \"tun\": %v", err)
	}
	defer func() {
		if err != nil {
			dev.Close()
		}
	}()

	dlog.Infof(c, "network interface %q has index %d", dev.Name(), dev.Index())

	// Place the DNS server in the private network at x.x.x.2
	dnsIP := make(net.IP, len(ifCIDR.IP))
	copy(dnsIP, ifCIDR.IP)
	dnsIP[len(dnsIP)-1] = 2

	// Configure interface with DNS and search domains, MTU, and subnet
	if err = dConn.SetLinkDNS(int(dev.Index()), dnsIP); err != nil {
		return nil, err
	}
	if err = dev.SetMTU(mtu); err != nil {
		return nil, err
	}

	// Use x.x.x.1 as the destination address
	toIP := make(net.IP, len(ifCIDR.IP))
	copy(toIP, ifCIDR.IP)
	toIP[len(toIP)-1] = 1
	if err = dev.AddSubnet(c, ifCIDR, toIP); err != nil {
		return nil, err
	}
	return &InterfaceWithDNS{
		Device: dev,
		ifIP:   ifCIDR.IP,
		dnsIP:  dnsIP,
	}, nil
}

func (t *InterfaceWithDNS) ForwardDNS(c context.Context, forwardAddr *net.UDPAddr, initDone *sync.WaitGroup) error {
	acs := make(chan *responder, 100)
	defer close(acs)

	t.responderPool = &sync.Pool{
		New: func() interface{} {
			return &responder{tun: t, buf: make([]byte, bufSize), raw: make([]byte, bufSize)}
		},
	}

	go t.forwardLoop(c, acs, forwardAddr)

	buf := make([]byte, bufSize)
	initDone.Done()

	for {
		n, err := t.File.Read(buf)
		if err != nil {
			if c.Err() == nil {
				err = fmt.Errorf("unable to read virtual network %s: %v", t.Name(), err)
			} else {
				err = nil
			}
			return err
		}
		rb := buf[:n]

		// Skip everything but ipv4 UDP requests to dnsIP port 53
		h, err := ipv4.ParseHeader(rb)
		if !(err == nil && h.Dst.Equal(t.dnsIP)) {
			continue
		}
		rb = rb[h.Len:]
		if udpDatagram(rb).destination() != 53 {
			continue
		}
		t.requestCount++
		ac := t.responderPool.Get().(*responder)
		copy(ac.buf, rb)
		acs <- ac
	}
}

func (t *InterfaceWithDNS) InterfaceIndex() int {
	return int(t.Index())
}

func (t *InterfaceWithDNS) RequestCount() uint64 {
	return t.requestCount
}

func (t *InterfaceWithDNS) forwardLoop(c context.Context, acs <-chan *responder, forwardAddr *net.UDPAddr) {
	for {
		var ac *responder
		select {
		case <-c.Done():
			for range acs {
			}
			return
		case ac = <-acs:
			if ac == nil {
				return
			}
		}
		ug := udpDatagram(ac.buf)
		srcPort := ug.source()

		// Need handler for DNS requests that will reply on the given source port
		lAddr, err := net.ResolveUDPAddr("udp4", "localhost:0")
		if nil != err {
			dlog.Errorf(c, "unable to resolve udp addr: %v", err)
			return
		}
		ac.port = srcPort
		ac.conn, err = net.ListenUDP("udp4", lAddr)
		if err != nil {
			dlog.Errorf(c, "unable to listen on UDP socket: %v", err)
			return
		}
		go ac.respond(dgroup.WithGoroutineName(c, "port "+strconv.Itoa(int(srcPort))))

		// Redirect the request to the internal DNS service
		_, err = ac.conn.WriteToUDP(ug.body(), forwardAddr)
		if err != nil && c.Err() == nil {
			dlog.Error(c, err)
		}
	}
}

func (t *InterfaceWithDNS) Name() string {
	return t.Name()
}

func (ac *responder) respond(c context.Context) {
	defer func() {
		ac.conn.Close()
		ac.tun.responderPool.Put(ac)
	}()

	// If no response is generated within a full second, then we're out of luck.
	err := ac.conn.SetReadDeadline(time.Now().Add(time.Second))
	if err != nil {
		if c.Err() == nil {
			dlog.Error(c, err)
		}
		return
	}

	n, _, err := ac.conn.ReadFromUDP(ac.buf)
	if err != nil {
		if c.Err() == nil {
			dlog.Error(c, err)
		}
		return
	}
	buf := ac.buf[:n]

	// Create the IP-packet containing the UDP datagram. redirect the response to port where the request originated.
	raw := ac.raw[:ipv4.HeaderLen+udpHeaderLen+n]
	h := &ipv4.Header{
		Version:  ipv4.Version,
		Len:      ipv4.HeaderLen,
		TOS:      0x0,
		TotalLen: len(raw),
		TTL:      64,
		Protocol: udpProto,
		Dst:      ac.tun.ifIP,
		Src:      ac.tun.dnsIP,
	}
	hb, _ := h.Marshal()
	binary.BigEndian.PutUint16(hb[10:12], checksum(hb)) // slightly faster than calling Marshal() again.

	copy(raw, hb)
	ug := udpDatagram(raw[ipv4.HeaderLen:])
	ug.setValues(53, ac.port, buf)
	ug.setChecksum(h.Src, h.Dst, udpProto)

	if _, err = ac.tun.File.Write(raw); err != nil && c.Err() == nil {
		dlog.Error(c, err)
	}
}
