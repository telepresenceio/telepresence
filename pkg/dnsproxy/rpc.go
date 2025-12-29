package dnsproxy

import (
	"github.com/miekg/dns"
	"google.golang.org/grpc/codes"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	grpcErrors "github.com/telepresenceio/telepresence/v2/pkg/grpc/errors"
)

func ToRPC(rrs RRs, rCode int) (*manager.DNSResponse, error) {
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
			return nil, grpcErrors.Errorf(codes.Internal, "unable to pack DNS reply: %v", err)
		}
	}
	rsp.Rrs = rrb
	return rsp, nil
}

func FromRPC(r *manager.DNSResponse) (RRs, int, error) {
	rrb := r.Rrs
	var rrs RRs
	off := 0
	for len(rrb) > off {
		var rr dns.RR
		var err error
		if rr, off, err = dns.UnpackRR(rrb, off); err != nil {
			return nil, dns.RcodeFormatError, grpcErrors.Errorf(codes.InvalidArgument, "unable to unpack DNS response: %v", err)
		}
		rrs = append(rrs, rr)
	}
	return rrs, int(r.RCode), nil
}
