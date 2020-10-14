package dns

import (
	_log "log"
	"net"
	"strings"

	"github.com/miekg/dns"
	"github.com/pkg/errors"

	"github.com/datawire/ambassador/pkg/supervisor"
)

type Server struct {
	Listeners []string
	Fallback  string
	Resolve   func(string) string
}

func log(line string, args ...interface{}) {
	_log.Printf("DNS: "+line, args...)
}

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
			ip = s.Resolve(domain)
		}
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
	log("QTYPE[%v] %s -> FALLBACK", r.Question[0].Qtype, domain)
	in, err := dns.Exchange(r, s.Fallback)
	if err != nil {
		log(err.Error())
		return
	}
	w.WriteMsg(in)
}

func (s *Server) Start(p *supervisor.Process) error {
	listeners := make([]net.PacketConn, len(s.Listeners))
	for i, addr := range s.Listeners {
		var err error
		listeners[i], err = net.ListenPacket("udp", addr)
		if err != nil {
			return errors.Wrap(err, "failed to set up udp listener")
		}
		log("listening on %s", addr)
	}
	for _, listener := range listeners {
		go func(listener net.PacketConn) {
			srv := &dns.Server{PacketConn: listener, Handler: s}
			if err := srv.ActivateAndServe(); err != nil {
				log("failed to active udp server: %v", err)
				p.Supervisor().Shutdown()
			}
		}(listener)
	}
	return nil
}
