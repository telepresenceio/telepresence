package manager

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/miekg/dns"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

const dnsTTL = 4

const arpaV4 = ".in-addr.arpa."
const arpaV6 = ".ip6.arpa."

func nibbleToInt(v string) (uint8, bool) {
	if len(v) != 1 {
		return 0, false
	}
	hd := v[0]
	if hd >= '0' && hd <= '9' {
		return hd - '0', true
	}
	if hd >= 'A' && hd <= 'F' {
		return 10 + hd - 'A', true
	}
	if hd >= 'a' && hd <= 'f' {
		return 10 + hd - 'a', true
	}
	return 0, false
}

func ptrAddress(addr string) (net.IP, error) {
	ip := iputil.Parse(addr)
	switch {
	case ip != nil:
		return ip, nil
	case strings.HasSuffix(addr, arpaV4):
		ix := addr[0 : len(addr)-len(arpaV4)]
		if ip = iputil.Parse(ix); ip != nil && len(ip) == 4 {
			return net.IP{ip[3], ip[2], ip[1], ip[0]}, nil
		}
		return nil, fmt.Errorf("%q is not a valid IP (v4) prefixing .in-addr.arpa", ix)
	case strings.HasSuffix(addr, arpaV6):
		hds := strings.Split(addr[0:len(addr)-len(arpaV6)], ".")
		if len(hds) != 32 {
			return nil, errors.New("expected 32 nibbles to prefix .ip6.arpa")
		}
		ip = make(net.IP, 16)
		odd := false
		for i, nb := range hds {
			d, ok := nibbleToInt(nb)
			if !ok {
				return nil, errors.New("expected 32 nibbles to prefix .ip6.arpa")
			}
			b := 15 - i>>1
			if odd {
				ip[b] |= d << 4
			} else {
				ip[b] = d
			}
			odd = !odd
		}
		return ip, nil
	default:
		return nil, fmt.Errorf("%q is neither a valid IP-address or a valid reverse notation", addr)
	}
}

func dnsLookup(ctx context.Context, qType uint16, qName string) ([]dns.RR, error) {
	var err error

	makeError := func(err error) ([]dns.RR, error) {
		dnsErr := net.DNSError{}
		if errors.Is(err, &dnsErr) {
			switch {
			case dnsErr.IsNotFound:
				return nil, status.Error(codes.NotFound, dnsErr.Error())
			case dnsErr.IsTemporary:
				return nil, status.Error(codes.Unavailable, dnsErr.Error())
			case dnsErr.IsTimeout:
				return nil, status.Error(codes.DeadlineExceeded, dnsErr.Error())
			}
		}
		return nil, status.Error(codes.Internal, err.Error())
	}

	rrReader := func() dns.RR_Header {
		return dns.RR_Header{Name: qName, Rrtype: qType, Class: dns.ClassINET, Ttl: dnsTTL}
	}

	var answer []dns.RR
	r := net.DefaultResolver
	switch qType {
	case dns.TypeA, dns.TypeAAAA:
		var ips iputil.IPs
		if ips, err = r.LookupIP(ctx, "ip", qName[:len(qName)-1]); err != nil {
			return makeError(err)
		}
		answer = make([]dns.RR, 0, len(ips))
		for _, ip := range ips {
			var rr dns.RR
			if ip4 := ip.To4(); ip4 != nil {
				rr = &dns.A{
					Hdr: rrReader(),
					A:   ip4,
				}
			} else if ip16 := ip.To16(); ip16 != nil {
				rr = &dns.AAAA{
					Hdr:  rrReader(),
					AAAA: ip16,
				}
			}
			answer = append(answer, rr)
		}
	case dns.TypePTR:
		var names []string
		ip, err := ptrAddress(qName)
		if err != nil {
			return makeError(err)
		}
		if names, err = r.LookupAddr(ctx, ip.String()); err != nil {
			return makeError(err)
		}
		answer = make([]dns.RR, len(names))
		for i, n := range names {
			answer[i] = &dns.PTR{
				Hdr: rrReader(),
				Ptr: n,
			}
		}
	case dns.TypeCNAME:
		var name string
		if name, err = r.LookupCNAME(ctx, qName); err != nil {
			return makeError(err)
		}
		answer = []dns.RR{&dns.CNAME{
			Hdr:    rrReader(),
			Target: name,
		}}
	case dns.TypeMX:
		var mx []*net.MX
		if mx, err = r.LookupMX(ctx, qName); err != nil {
			return makeError(err)
		}
		answer = make([]dns.RR, len(mx))
		for i, r := range mx {
			answer[i] = &dns.MX{
				Hdr:        rrReader(),
				Preference: r.Pref,
				Mx:         r.Host,
			}
		}
	case dns.TypeNS:
		var ns []*net.NS
		if ns, err = r.LookupNS(ctx, qName); err != nil {
			return makeError(err)
		}
		answer = make([]dns.RR, len(ns))
		for i, n := range ns {
			answer[i] = &dns.NS{
				Hdr: rrReader(),
				Ns:  n.Host,
			}
		}
	case dns.TypeSRV:
		var srvs []*net.SRV
		if _, srvs, err = r.LookupSRV(ctx, "", "", qName); err != nil {
			return makeError(err)
		}
		answer = make([]dns.RR, len(srvs))
		for i, s := range srvs {
			answer[i] = &dns.SRV{
				Hdr:      rrReader(),
				Target:   s.Target,
				Port:     s.Port,
				Priority: s.Priority,
				Weight:   s.Weight,
			}
		}
	case dns.TypeTXT:
		var names []string
		if names, err = r.LookupTXT(ctx, qName[:len(qName)-1]); err != nil {
			return makeError(err)
		}
		answer = []dns.RR{&dns.TXT{
			Hdr: rrReader(),
			Txt: names,
		}}
	default:
		return nil, status.Errorf(codes.Unimplemented, "unsupported DNS query type %s", dns.TypeToString[qType])
	}
	return answer, nil
}
