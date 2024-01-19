package dnsproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

const dnsTTL = 4

const (
	arpaV4 = ".in-addr.arpa."
	arpaV6 = ".ip6.arpa."
)

type RRs []dns.RR

func SupportedType(qType uint16) bool {
	switch qType {
	case dns.TypeA, dns.TypeAAAA, dns.TypePTR, dns.TypeCNAME, dns.TypeMX, dns.TypeNS, dns.TypeSRV, dns.TypeTXT:
		return true
	default:
		return false
	}
}

func writeRR(rr dns.RR, bf *strings.Builder) {
	switch rr := rr.(type) {
	case *dns.A:
		bf.WriteString(rr.A.String())
	case *dns.AAAA:
		bf.WriteString(rr.AAAA.String())
	case *dns.PTR:
		bf.WriteString(rr.Ptr)
	case *dns.CNAME:
		bf.WriteString(rr.Target)
	case *dns.MX:
		fmt.Fprintf(bf, "%s(pref %d)", rr.Mx, rr.Preference)
	case *dns.NS:
		bf.WriteString(rr.Ns)
	case *dns.SRV:
		fmt.Fprintf(bf, "%s(port %d, prio %d, weight %d)", rr.Target, rr.Port, rr.Priority, rr.Weight)
	case *dns.TXT:
		bf.WriteString(strings.Join(rr.Txt, ","))
	default:
		bf.WriteString(rr.String())
	}
}

func (a RRs) String() string {
	if len(a) == 0 {
		return "EMPTY"
	}
	bf := strings.Builder{}
	bf.WriteByte('[')
	for i, rr := range a {
		if i > 0 {
			bf.WriteByte(',')
		}
		writeRR(rr, &bf)
	}
	bf.WriteByte(']')
	return bf.String()
}

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

func PtrAddress(addr string) (net.IP, error) {
	ip := iputil.Parse(addr)
	switch {
	case ip != nil:
		return ip, nil
	case strings.HasSuffix(addr, arpaV4):
		ix := addr[0 : len(addr)-len(arpaV4)]
		if ip = iputil.Parse(ix); len(ip) == 4 {
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

func NewHeader(qName string, qType uint16) dns.RR_Header {
	return dns.RR_Header{Name: qName, Rrtype: qType, Class: dns.ClassINET, Ttl: dnsTTL}
}

// useLookupName takes care of an undocumented "feature" in some lookup functions.
// If the name ends with a dot, then no search path will be applied. If however,
// the name doesn't end with a dot, the search path is always applied and the name
// is never used verbatim.
func useLookupName(qName string) (string, bool) {
	dots := 0
	name := qName[:len(qName)-1]
	for _, c := range qName {
		if c == '.' {
			dots++
		}
	}
	switch dots {
	case 1:
		// singleton name, it's safe to assume that a search path must be applied
		return name, true
	case 2, 3, 4:
		// might need a search path, or might be a full name.
		return name, false
	default:
		// With > 4 dots, we can safely assume that no search path should be applied
		return qName, true
	}
}

// TimedExternalLookup will shell out to an operating specific lookup command. The reason for this
// is to make sure that no caching or a negative result is performed in this process, which would
// invalidate subsequent attempts.
func TimedExternalLookup(ctx context.Context, name string, timeout time.Duration) iputil.IPs {
	return externalLookup(ctx, name, timeout)
}

func lookupIP(ctx context.Context, network, qName string, r *net.Resolver) ([]net.IP, error) {
	name, final := useLookupName(qName)
	ips, err := r.LookupIP(ctx, network, name)
	if err != nil && !final {
		dlog.Errorf(ctx, "LookupIP failed, trying LookupIP %q", qName)
		ips, err = r.LookupIP(ctx, network, qName)
	}
	if err == nil && len(ips) == 0 {
		err = &net.DNSError{
			Err:        "no such host",
			Name:       name,
			IsNotFound: true,
		}
	}
	return ips, err
}

func makeError(err error) (RRs, int, error) {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		switch {
		case dnsErr.IsNotFound:
			return nil, dns.RcodeNameError, nil
		case dnsErr.IsTemporary:
			return nil, dns.RcodeNameError, status.Error(codes.Unavailable, dnsErr.Error())
		case dnsErr.IsTimeout:
			return nil, dns.RcodeNameError, status.Error(codes.DeadlineExceeded, dnsErr.Error())
		}
	}
	return nil, dns.RcodeServerFailure, status.Error(codes.Internal, err.Error())
}

func Lookup(ctx context.Context, qType uint16, qName string) (RRs, int, error) {
	var answer RRs
	r := &net.Resolver{StrictErrors: true}
	switch qType {
	case dns.TypeA, dns.TypeAAAA:
		ips, err := lookupIP(ctx, "ip", qName, r)
		if err != nil {
			return makeError(err)
		}
		for _, ip := range ips {
			if ip4 := ip.To4(); ip4 != nil {
				if qType == dns.TypeA {
					answer = append(answer, &dns.A{
						Hdr: NewHeader(qName, qType),
						A:   ip4,
					})
				}
			} else if ip16 := ip.To16(); ip16 != nil && qType == dns.TypeAAAA {
				answer = append(answer, &dns.AAAA{
					Hdr:  NewHeader(qName, qType),
					AAAA: ip16,
				})
			}
		}
	case dns.TypePTR:
		var names []string
		ip, err := PtrAddress(qName)
		if err != nil {
			return makeError(err)
		}
		if names, err = r.LookupAddr(ctx, ip.String()); err != nil {
			return makeError(err)
		}
		answer = make(RRs, len(names))
		for i, n := range names {
			answer[i] = &dns.PTR{
				Hdr: NewHeader(qName, qType),
				Ptr: n,
			}
		}
	case dns.TypeCNAME:
		name, final := useLookupName(qName)
		target, err := r.LookupCNAME(ctx, name)
		if err != nil && !final {
			target, err = r.LookupCNAME(ctx, qName)
		}
		if err != nil {
			return makeError(err)
		}
		answer = RRs{&dns.CNAME{
			Hdr:    NewHeader(qName, qType),
			Target: target,
		}}
	case dns.TypeMX:
		mx, err := r.LookupMX(ctx, qName[:len(qName)-1])
		if err != nil {
			return makeError(err)
		}
		answer = make(RRs, len(mx))
		for i, r := range mx {
			answer[i] = &dns.MX{
				Hdr:        NewHeader(qName, qType),
				Preference: r.Pref,
				Mx:         r.Host,
			}
		}
	case dns.TypeNS:
		ns, err := r.LookupNS(ctx, qName[:len(qName)-1])
		if err != nil {
			return makeError(err)
		}
		answer = make(RRs, len(ns))
		for i, n := range ns {
			answer[i] = &dns.NS{
				Hdr: NewHeader(qName, qType),
				Ns:  n.Host,
			}
		}
	case dns.TypeSRV:
		_, srvs, err := r.LookupSRV(ctx, "", "", qName[:len(qName)-1])
		if err != nil {
			rrs, rCode, err := makeError(err)
			if rCode != dns.RcodeNameError {
				return rrs, rCode, err
			}
			// The LookupSRV doesn't use libc for the lookup even when told to do so, amd normal
			// search-path expansion doesn't seem to apply. Let's see if the FQN is different, and
			// if so, try that instead.
			fqn := svcFQN(ctx, qName, r)
			if fqn == "" || fqn == qName {
				return rrs, rCode, err
			}
			var fqnErr error
			if _, srvs, fqnErr = r.LookupSRV(ctx, "", "", fqn); fqnErr != nil {
				// Return original error
				return rrs, rCode, err
			}
		}
		answer = make(RRs, len(srvs))
		for i, s := range srvs {
			answer[i] = &dns.SRV{
				Hdr:      NewHeader(qName, qType),
				Target:   s.Target,
				Port:     s.Port,
				Priority: s.Priority,
				Weight:   s.Weight,
			}
		}
	case dns.TypeTXT:
		names, err := r.LookupTXT(ctx, qName)
		if err != nil {
			return makeError(err)
		}
		answer = RRs{&dns.TXT{
			Hdr: NewHeader(qName, qType),
			Txt: names,
		}}
	default:
		return nil, dns.RcodeNotImplemented, status.Errorf(codes.Unimplemented, "unsupported DNS query type %s", dns.TypeToString[qType])
	}
	return answer, dns.RcodeSuccess, nil
}

func svcFQN(ctx context.Context, name string, r *net.Resolver) string {
	parts := strings.Split(name, ".")
	if !(len(parts) > 2 && strings.HasPrefix(parts[0], "_") && strings.HasPrefix(parts[1], "_")) {
		return ""
	}
	svcName := strings.Join(parts[2:], ".")
	ips, err := r.LookupIP(ctx, "ip", svcName[:len(svcName)-1])
	if err != nil || len(ips) < 1 {
		return ""
	}
	names, err := r.LookupAddr(ctx, ips[0].String())
	if err != nil || len(names) < 1 {
		return ""
	}
	fqn := names[0]
	ix := strings.Index(fqn, svcName)
	if ix < 0 {
		return ""
	}
	fqn = parts[0] + "." + parts[1] + "." + fqn[ix:]
	return fqn
}
