package dns

import (
	"log"
	"net"
	"strings"
	"github.com/miekg/dns"
)

type Server struct {
	Listeners []string
	Fallback string
	Resolve func(string) string
}

func (s *Server) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	log.Println(r.Question[0].Qtype, "DNS request for", r.Question[0].Name)
	domain := strings.ToLower(r.Question[0].Name)
	switch r.Question[0].Qtype {
	case dns.TypeA:
		log.Println("Looking up", domain)
		ip := s.Resolve(domain)
		if ip != "" {
			log.Println("Found:", domain)
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
				Hdr: dns.RR_Header{ Name: r.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60 },
				A: net.ParseIP(ip),
			})
			w.WriteMsg(&msg)
			return
		}
	default:
		ip := s.Resolve(domain)
		if ip != "" {
			log.Println("Found:", domain)
			msg := dns.Msg{}
			msg.SetReply(r)
			msg.Authoritative = true
			msg.RecursionAvailable = true
			w.WriteMsg(&msg)
			log.Println("replied with empty")
			return
		}
	}
	in, err := dns.Exchange(r, s.Fallback)
	if err != nil {
		log.Println(err)
		return
	}
	w.WriteMsg(in)
}

func (s *Server) Start() {
	for _, addr := range s.Listeners {
		go func(addr string) {
			srv := &dns.Server{Addr: addr, Net: "udp"}
			srv.Handler = s
			log.Printf("DNS server listening on %s", addr)
			if err := srv.ListenAndServe(); err != nil {
				log.Fatalf("Failed to set udp listener %s\n", err.Error())
			}
		}(addr)
	}
}
