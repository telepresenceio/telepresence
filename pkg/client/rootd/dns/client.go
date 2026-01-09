package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/miekg/dns"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type luResult struct {
	netip.Addr
	rCode int
}

// LookupIP performs an A and an AAAA query and returns the first answer.
func LookupIP(ctx context.Context, localDNS netip.AddrPort, name string) (ip netip.Addr, err error) {
	c := new(dns.Client)
	ctx, cancel := context.WithTimeout(ctx, client.GetConfig(ctx).DNS().LookupTimeout)
	defer cancel()

	dnsAddr := localDNS.String()
	qName := dns.Fqdn(name)
	ch := make(chan luResult, 2)

	go lookupIP(ctx, c, dnsAddr, qName, dns.TypeA, ch)
	go lookupIP(ctx, c, dnsAddr, qName, dns.TypeAAAA, ch)

	for i := 0; i < 2; i++ {
		select {
		case <-ctx.Done():
			return ip, status.Error(codes.Canceled, ctx.Err().Error())
		case r := <-ch:
			switch r.rCode {
			case dns.RcodeSuccess:
				if r.IsValid() {
					return r.Addr, nil
				}
			case dns.RcodeNameError:
			default:
				return ip, status.Error(codes.Internal, fmt.Sprintf("unable to resolve name %s: %s", name, dns.RcodeToString[r.rCode]))
			}
		}
	}
	return ip, status.Error(codes.NotFound, fmt.Sprintf("unable to resolve name %s", name))
}

func lookupIP(ctx context.Context, c *dns.Client, localDNS, name string, qType uint16, ch chan<- luResult) {
	m := new(dns.Msg)
	m.SetQuestion(name, qType)
	r, _, err := c.ExchangeContext(ctx, m, localDNS)
	if err != nil {
		rCode := dns.RcodeServerFailure
		var opErr *net.OpError
		switch {
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			rCode = dns.RcodeNameError
		case errors.As(err, &opErr) && opErr.Timeout():
			rCode = dns.RcodeNameError
		default:
			clog.Errorf(ctx, "dns.ExchangeContext: %v", err)
		}
		ch <- luResult{rCode: rCode}
		return
	}
	if r.Rcode != dns.RcodeSuccess {
		ch <- luResult{rCode: r.Rcode}
		return
	}
	for _, rr := range r.Answer {
		if rr.Header().Rrtype == qType {
			var bs net.IP
			switch qType {
			case dns.TypeA:
				bs = rr.(*dns.A).A
			case dns.TypeAAAA:
				bs = rr.(*dns.AAAA).AAAA
			}
			if len(bs) > 0 {
				if ip, ok := netip.AddrFromSlice(bs); ok {
					ch <- luResult{Addr: ip, rCode: dns.RcodeSuccess}
					return
				}
			}
		}
	}
	ch <- luResult{rCode: dns.RcodeSuccess}
}
