package dns

import (
	"net"
	"strings"

	"github.com/datawire/ambassador/pkg/supervisor"
	"github.com/miekg/dns"
	"github.com/pkg/errors"
)

// Server is a DNS server which implements the github.com/miekg/dns Handler interface
type Server struct {
	p         *supervisor.Process
	listeners []string
	fallback  string
	resolve   func(string) string
}

// NewServer returns a new dns.Server
func NewServer(p *supervisor.Process, listeners []string, fallback string, resolve func(string) string) *Server {
	return &Server{
		p:         p,
		listeners: listeners,
		fallback:  fallback,
		resolve:   resolve,
	}
}

// ServeDNS is an implementation of github.com/miekg/dns Handler.ServeDNS.
func (s *Server) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	domain := strings.ToLower(r.Question[0].Name)
	switch r.Question[0].Qtype {
	case dns.TypeA:
		var ip string
		if domain == "localhost." {
			// BUG(lukeshu): I have no idea why a lookup
			// for localhost even makes it to here on my
			// home WiFi when connecting to a k3sctl
			// cluster (but not a kubernaut.io cluster).
			// But it does, so I need this in order to be
			// productive at home.  We should really
			// root-cause this, because it's weird.
			ip = "127.0.0.1"
		} else {
			ip = s.resolve(domain)
		}
		if ip != "" {
			s.p.Logf("QUERY %s -> %s", domain, ip)
			msg := dns.Msg{}
			msg.SetReply(r)
			msg.Authoritative = true
			// mac dns seems to fallback if you don't
			// support recursion, if you have more than a
			// single dns server, this will prevent us
			// from intercepting all queries
			msg.RecursionAvailable = true
			// if we don't give back the same domain
			// requested, then mac dns seems to return an
			// nxdomain
			msg.Answer = append(msg.Answer, &dns.A{
				Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
				A:   net.ParseIP(ip),
			})
			_ = w.WriteMsg(&msg)
			return
		}
	default:
		ip := s.resolve(domain)
		if ip != "" {
			s.p.Logf("QTYPE[%v] %s -> EMPTY", r.Question[0].Qtype, domain)
			msg := dns.Msg{}
			msg.SetReply(r)
			msg.Authoritative = true
			msg.RecursionAvailable = true
			_ = w.WriteMsg(&msg)
			return
		}
	}
	s.p.Logf("QTYPE[%v] %s -> FALLBACK", r.Question[0].Qtype, domain)
	in, err := dns.Exchange(r, s.fallback)
	if err != nil {
		s.p.Log(err.Error())
		return
	}
	_ = w.WriteMsg(in)
}

// Start starts the DNS server
func (s *Server) Start() error {
	listeners := make([]net.PacketConn, len(s.listeners))
	for i, addr := range s.listeners {
		var err error
		listeners[i], err = net.ListenPacket("udp", addr)
		if err != nil {
			return errors.Wrap(err, "failed to set up udp listener")
		}
		s.p.Logf("listening on %s", addr)
	}
	for _, listener := range listeners {
		go func(listener net.PacketConn) {
			srv := &dns.Server{PacketConn: listener, Handler: s}
			if err := srv.ActivateAndServe(); err != nil {
				s.p.Logf("failed to active udp server: %v", err)
				s.p.Supervisor().Shutdown()
			}
		}(listener)
	}
	return nil
}
