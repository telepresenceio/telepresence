package rootd

import (
	"context"
	"net"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

const dnsConnTTL = 5 * time.Second

func (s *Session) isForDNS(ip net.IP, port uint16) bool {
	return s.remoteDnsIP != nil && port == 53 && s.remoteDnsIP.Equal(ip)
}

func (s *Session) streamCreator() tunnel.StreamCreator {
	return func(c context.Context, id tunnel.ConnID) (tunnel.Stream, error) {
		p := id.Protocol()
		if p == ipproto.UDP && s.isForDNS(id.Destination(), id.DestinationPort()) {
			pipeId := tunnel.NewConnID(p, id.Source(), s.dnsLocalAddr.IP, id.SourcePort(), uint16(s.dnsLocalAddr.Port))
			dlog.Tracef(c, "Intercept DNS %s to %s", id, pipeId.DestinationAddr())
			from, to := tunnel.NewPipe(pipeId, s.session.SessionId)
			tunnel.NewDialerTTL(to, func() {}, dnsConnTTL, nil, nil).Start(c)
			return from, nil
		}
		if p == ipproto.TCP {
			if pf := dnet.GetPortForwardDialer(c); pf != nil {
				if stream := s.tryPortForward(c, id, pf); stream != nil {
					return stream, nil
				}
			} else {
				dlog.Debug(c, "Found no port-forward dialer in context")
			}
		}
		dlog.Debugf(c, "Opening tunnel for id %s", id)
		ct, err := s.managerClient.Tunnel(c)
		if err != nil {
			return nil, err
		}
		tc := client.GetConfig(c).Timeouts()
		return tunnel.NewClientStream(c, ct, id, s.session.SessionId, tc.Get(client.TimeoutRoundtripLatency), tc.Get(client.TimeoutEndpointDial))
	}
}

func (s *Session) tryPortForward(c context.Context, id tunnel.ConnID, pf dnet.PortForwardDialer) tunnel.Stream {
	pfr, err := s.managerClient.GetPortForwardPod(c, &manager.PortForwardPodRequest{ConnId: []byte(id)})
	switch status.Code(err) {
	case codes.OK:
		conn, err := pf.DialPod(c, pfr.Name, pfr.Namespace, uint16(pfr.Port))
		if err == nil {
			dlog.Debugf(c, "Using port-forward to %s.%s:%d for %s", pfr.Name, pfr.Namespace, pfr.Port, id.DestinationAddr())
			tc := client.GetConfig(c).Timeouts()
			return tunnel.NewConnStream(conn, id, s.session.SessionId, tc.Get(client.TimeoutRoundtripLatency), tc.Get(client.TimeoutEndpointDial))
		}
		dlog.Debugf(c, "Unable to use direct-port forward to %s: DialPod returned %v", id.DestinationAddr(), err)
	case codes.Unimplemented:
		dlog.Debug(c, "Direct port forward is not implemented by the traffic-manager")
	case codes.Unavailable:
		dlog.Debug(c, "Direct port forward is disabled by the traffic-manager")
	default:
		dlog.Errorf(c, "Direct port forward: GetPortForwardPod errors with %v", err)
	}
	return nil
}
