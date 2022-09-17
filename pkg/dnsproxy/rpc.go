package dnsproxy

import (
	"github.com/blang/semver"
	"github.com/miekg/dns"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func ManagerCanDoDNSQueryTypes(v semver.Version) bool {
	return v.Major > 2 || v.Major == 2 && v.Minor > 7
}

func ToRPC(rrs []dns.RR, rCode int) (*manager.DNSResponse, error) {
	l := 0
	for _, rr := range rrs {
		l += dns.Len(rr)
	}
	rsp := &manager.DNSResponse{RCode: int32(rCode)}
	rrb := make([]byte, l)
	off := 0
	for _, rr := range rrs {
		var err error
		if off, err = dns.PackRR(rr, rrb, off, nil, false); err != nil {
			return nil, status.Errorf(codes.Internal, "unable to pack DNS reply: %v", err)
		}
	}
	rsp.Rrs = rrb
	return rsp, nil
}

func FromRPC(r *manager.DNSResponse) ([]dns.RR, int, error) {
	rrb := r.Rrs
	var rrs []dns.RR
	off := 0
	for len(rrb) > off {
		var rr dns.RR
		var err error
		if rr, off, err = dns.UnpackRR(rrb, off); err != nil {
			return nil, dns.RcodeFormatError, status.Errorf(codes.InvalidArgument, "unable to unpack DNS response: %v", err)
		}
		rrs = append(rrs, rr)
	}
	return rrs, int(r.RCode), nil
}
