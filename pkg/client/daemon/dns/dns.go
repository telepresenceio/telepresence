package dns

import (
	"context"
	"net"
	"strings"
	"sync/atomic"

	"github.com/miekg/dns"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
)

// Server is a DNS server which implements the github.com/miekg/dns Handler interface
type Server struct {
	ctx          context.Context // necessary to make logging work in ServeDNS function
	listeners    []net.PacketConn
	fallback     *dns.Conn
	resolve      func(string) []net.IP
	requestCount int64
}

// NewServer returns a new dns.Server
func NewServer(c context.Context, listeners []net.PacketConn, fallback *dns.Conn, resolve func(string) []net.IP) *Server {
	return &Server{
		ctx:       c,
		listeners: listeners,
		fallback:  fallback,
		resolve:   resolve,
	}
}

// RequestCount returns the number of requests that this server has received.
func (s *Server) RequestCount() int {
	return int(atomic.LoadInt64(&s.requestCount))
}

// ServeDNS is an implementation of github.com/miekg/dns Handler.ServeDNS.
func (s *Server) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	c := s.ctx
	defer func() {
		// Closing the response tells the DNS service to terminate
		if c.Err() != nil {
			_ = w.Close()
		}
	}()

	atomic.AddInt64(&s.requestCount, 1)
	domain := strings.ToLower(r.Question[0].Name)
	switch r.Question[0].Qtype {
	case dns.TypeA:
		var ips []net.IP
		if domain == "localhost." {
			// BUG(lukeshu): I have no idea why a lookup
			// for localhost even makes it to here on my
			// home WiFi when connecting to a k3sctl
			// cluster (but not a kubernaut.io cluster).
			// But it does, so I need this in order to be
			// productive at home.  We should really
			// root-cause this, because it's weird.
			ips = []net.IP{{127, 0, 0, 1}}
		} else {
			ips = s.resolve(domain)
		}
		if len(ips) > 0 {
			msg := dns.Msg{}
			msg.SetReply(r)
			msg.Authoritative = true
			// mac dns seems to fallback if you don't
			// support recursion, if you have more than a
			// single dns server, this will prevent us
			// from intercepting all queries
			msg.RecursionAvailable = true
			for _, ip := range ips {
				dlog.Debugf(c, "QUERY %s -> %s", domain, ip)
				// if we don't give back the same domain
				// requested, then mac dns seems to return an
				// nxdomain
				msg.Answer = append(msg.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: domain, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
					A:   ip,
				})
			}
			_ = w.WriteMsg(&msg)
			return
		}
	default:
		ips := s.resolve(domain)
		if len(ips) > 0 {
			dlog.Debugf(c, "QTYPE[%v] %s -> EMPTY", r.Question[0].Qtype, domain)
			msg := dns.Msg{}
			msg.SetReply(r)
			msg.Authoritative = true
			msg.RecursionAvailable = true
			_ = w.WriteMsg(&msg)
			return
		}
	}
	if s.fallback != nil {
		dlog.Debugf(c, "QTYPE[%v] %s -> FALLBACK", r.Question[0].Qtype, domain)
		client := dns.Client{Net: "udp"}
		in, _, err := client.ExchangeWithConn(r, s.fallback)
		if err != nil {
			dlog.Error(c, err)
			return
		}
		_ = w.WriteMsg(in)
	} else {
		dlog.Debugf(c, "QTYPE[%v] %s -> NOT FOUND", r.Question[0].Qtype, domain)
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeNameError)
		_ = w.WriteMsg(m)
	}
}

// Start starts the DNS server
func (s *Server) Run(c context.Context) error {
	g := dgroup.NewGroup(c, dgroup.GroupConfig{})
	for _, listener := range s.listeners {
		srv := &dns.Server{PacketConn: listener, Handler: s}
		g.Go(listener.LocalAddr().String(), func(c context.Context) error {
			go func() {
				<-c.Done()
				_ = srv.ShutdownContext(dcontext.HardContext(c))
			}()
			return srv.ActivateAndServe()
		})
	}
	return g.Wait()
}
