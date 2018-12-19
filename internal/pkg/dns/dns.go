package dns

import (
	"github.com/miekg/dns"
	_log "log"
	"net"
	"strings"
)

type Server struct {
	Listeners []string
	Fallback  string
	Resolve   func(string) string
}

func log(line string, args ...interface{}) {
	_log.Printf("DNS: "+line, args...)
}

func die(line string, args ...interface{}) {
	_log.Fatalf("DNS: "+line, args...)
}

func (s *Server) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	domain := strings.ToLower(r.Question[0].Name)
	switch r.Question[0].Qtype {
	case dns.TypeA:
		ip := s.Resolve(domain)
		if ip != "" {
			log("QUERY %s -> %s", domain, ip)
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
			w.WriteMsg(&msg)
			return
		}
	default:
		ip := s.Resolve(domain)
		if ip != "" {
			log("QTYPE[%v] %s -> EMPTY", r.Question[0].Qtype, domain)
			msg := dns.Msg{}
			msg.SetReply(r)
			msg.Authoritative = true
			msg.RecursionAvailable = true
			w.WriteMsg(&msg)
			return
		}
	}
	in, err := dns.Exchange(r, s.Fallback)
	if err != nil {
		log(err.Error())
		return
	}
	w.WriteMsg(in)
}

func (s *Server) Start() {
	listeners := make([]net.PacketConn, len(s.Listeners))
	for i, addr := range s.Listeners {
		var err error
		listeners[i], err = net.ListenPacket("udp", addr)
		if err != nil {
			die("failed to set up udp listener: %v", err)
		}
		log("listening on %s", addr)
	}
	for _, listener := range listeners {
		go func(listener net.PacketConn) {
			srv := &dns.Server{PacketConn: listener, Handler: s}
			if err := srv.ActivateAndServe(); err != nil {
				die("failed to set udp listener: %v", err)
			}
		}(listener)
	}
}
