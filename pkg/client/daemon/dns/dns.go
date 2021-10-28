package dns

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
)

type Resolver func(ctx context.Context, domain string) []net.IP

// recursionCheck is a special host name in a well known namespace that isn't expected to exist. It
// is used once for determining if the cluster's DNS resolver will call the Telepresence DNS resolver
// recursively. This is common when the cluster is running on the local host (k3s in docker for instance).
const recursionCheck = "tel2-recursion-check.kube-system"

// Server is a DNS server which implements the github.com/miekg/dns Handler interface
type Server struct {
	ctx          context.Context // necessary to make logging work in ServeDNS function
	listeners    []net.PacketConn
	fallback     *dns.Conn
	resolve      Resolver
	requestCount int64
	cache        *sync.Map
	recursive    int32 // 0 = never tested, 1 = not recursive, 2 = recursive
	cacheResolve func(*dns.Question) []dns.RR
}

type dnsValue struct {
	created   time.Time
	recursion int32 // will be set to the current qType during call to cluster
	answer    []dns.RR
	wait      chan struct{}
}

// NewServer returns a new dns.Server
func NewServer(listeners []net.PacketConn, fallback *dns.Conn, resolve Resolver, cache *sync.Map) *Server {
	s := &Server{
		listeners: listeners,
		fallback:  fallback,
		resolve:   resolve,
		cache:     cache,
	}
	s.cacheResolve = s.resolveWithRecursionCheck
	return s
}

// RequestCount returns the number of requests that this server has received.
func (s *Server) RequestCount() int {
	return int(atomic.LoadInt64(&s.requestCount))
}

func copyRRs(rrs []dns.RR, qType uint16) []dns.RR {
	if len(rrs) == 0 {
		return rrs
	}
	cp := make([]dns.RR, 0, len(rrs))
	for _, rr := range rrs {
		if rr.Header().Rrtype == qType {
			cp = append(cp, dns.Copy(rr))
		}
	}
	return cp
}

// resolveThruCache resolves the given query by first performing a cache lookup. If a cached
// entry is found that hasn't expired, it's returned. If not, this function will call
// resolveQuery() to resolve and store in the case.
func (s *Server) resolveThruCache(q *dns.Question) []dns.RR {
	newDv := &dnsValue{wait: make(chan struct{}), created: time.Now()}
	if v, loaded := s.cache.LoadOrStore(q.Name, newDv); loaded {
		oldDv := v.(*dnsValue)
		if atomic.LoadInt32(&s.recursive) == 2 && atomic.LoadInt32(&oldDv.recursion) == int32(q.Qtype) {
			// We have to assume that this is a recursion from the cluster.
			return nil
		}
		<-oldDv.wait
		if time.Since(oldDv.created) < 60*time.Second {
			return copyRRs(oldDv.answer, q.Qtype)
		}
		s.cache.Store(q.Name, newDv)
	}
	return s.resolveQuery(q, newDv)
}

// resolveWithRecursionCheck is a special version of resolveThruCache which is only used until the
// recursionCheck query has completed, and it has been determined whether a query that is propagated
// to the cluster will recurse back to this resolver or not.
func (s *Server) resolveWithRecursionCheck(q *dns.Question) []dns.RR {
	newDv := &dnsValue{wait: make(chan struct{}), created: time.Now()}
	if v, loaded := s.cache.LoadOrStore(q.Name, newDv); loaded {
		oldDv := v.(*dnsValue)
		if atomic.LoadInt32(&oldDv.recursion) == int32(q.Qtype) {
			if q.Name == recursionCheck+"." {
				atomic.StoreInt32(&s.recursive, 2)
			}
			if atomic.LoadInt32(&s.recursive) != 1 {
				return nil
			}
		}
		<-oldDv.wait
		if time.Since(oldDv.created) < 60*time.Second {
			return copyRRs(oldDv.answer, q.Qtype)
		}
		s.cache.Store(q.Name, newDv)
	}

	answer := s.resolveQuery(q, newDv)
	if q.Name == recursionCheck+"." {
		if atomic.LoadInt32(&s.recursive) == 2 {
			dlog.Debug(s.ctx, "DNS resolver is recursive")
		} else {
			atomic.StoreInt32(&s.recursive, 1)
			dlog.Debug(s.ctx, "DNS resolver is not recursive")
		}
		s.cacheResolve = s.resolveThruCache
	}
	return answer
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

	q := &r.Question[0]
	if atomic.CompareAndSwapInt64(&s.requestCount, 0, 1) {
		// Perform the first recursion check query
		go func() {
			dlog.Debugf(c, "Performing initial recursion check with %s", recursionCheck)
			_, err := net.DefaultResolver.LookupHost(c, recursionCheck)
			dlog.Debugf(c, "recursion check ended with %v", err)
		}()
	} else {
		atomic.AddInt64(&s.requestCount, 1)
	}

	if answer := s.cacheResolve(q); answer != nil {
		switch len(answer) {
		case 0:
			dlog.Debugf(c, "QTYPE[%v] %s -> EMPTY", q.Qtype, q.Name)
		case 1:
			dlog.Debugf(c, "QTYPE[%v] %s -> %s", q.Qtype, q.Name, answer[0])
		default:
			dlog.Debugf(c, "QTYPE[%v] %s -> %v", q.Qtype, q.Name, answer)
		}
		msg := new(dns.Msg)
		msg.SetReply(r)
		msg.Answer = answer
		msg.Authoritative = true
		// mac dns seems to fallback if you don't
		// support recursion, if you have more than a
		// single dns server, this will prevent us
		// from intercepting all queries
		msg.RecursionAvailable = true
		_ = w.WriteMsg(msg)
	} else {
		if s.fallback != nil {
			dlog.Debugf(c, "QTYPE[%v] %s -> FALLBACK", q.Qtype, q.Name)
			client := dns.Client{Net: "udp"}
			in, _, err := client.ExchangeWithConn(r, s.fallback)
			if err != nil {
				dlog.Error(c, err)
				return
			}
			_ = w.WriteMsg(in)
		} else {
			dlog.Debugf(c, "QTYPE[%v] %s -> NOT FOUND", q.Qtype, q.Name)
			m := new(dns.Msg)
			m.SetRcode(r, dns.RcodeNameError)
			_ = w.WriteMsg(m)
		}
	}
}

func (s *Server) resolveQuery(q *dns.Question, dv *dnsValue) []dns.RR {
	atomic.StoreInt32(&dv.recursion, int32(q.Qtype))
	defer func() {
		atomic.StoreInt32(&dv.recursion, 0)
		close(dv.wait)
	}()

	switch q.Qtype {
	case dns.TypeA, dns.TypeAAAA:
		ips := s.resolve(s.ctx, q.Name)
		if len(ips) == 0 {
			break
		}

		// The host is known. Return a result for the correct query type. The result might be empty for the given
		// query type and that is OK.
		// See https://datatracker.ietf.org/doc/html/rfc4074#section-3
		answer := make([]dns.RR, 0, len(ips))
		for _, ip := range ips {
			// if we don't give back the same domain
			// requested, then mac dns seems to return an
			// nxdomain
			var rr dns.RR
			if ip4 := ip.To4(); ip4 != nil {
				rr = &dns.A{
					Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 5},
					A:   ip4,
				}
			} else {
				rr = &dns.AAAA{
					Hdr:  dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 5},
					AAAA: ip,
				}
			}
			answer = append(answer, rr)
		}
		dv.answer = answer
	default:
		ips := s.resolve(s.ctx, q.Name)
		if len(ips) > 0 {
			dv.answer = []dns.RR{}
		}
	}
	if len(dv.answer) == 0 {
		s.cache.Delete(q.Name) // Don't cache unless the entry is found.
	}
	return copyRRs(dv.answer, q.Qtype)
}

// Run starts the DNS server(s) and waits for them to end
func (s *Server) Run(c context.Context, initDone chan<- struct{}) error {
	s.ctx = c
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
	close(initDone)
	return g.Wait()
}
