package rootd

import (
	"context"
	"net"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

const dnsConnTTL = 5 * time.Second

func (s *session) isForDNS(ip net.IP, port uint16) bool {
	return s.remoteDnsIP != nil && port == 53 && s.remoteDnsIP.Equal(ip)
}

func (s *session) streamCreator() tunnel.StreamCreator {
	return func(c context.Context, id tunnel.ConnID) (tunnel.Stream, error) {
		p := id.Protocol()
		if p == ipproto.UDP && s.isForDNS(id.Destination(), id.DestinationPort()) {
			pipeId := tunnel.NewConnID(p, id.Source(), s.dnsLocalAddr.IP, id.SourcePort(), uint16(s.dnsLocalAddr.Port))
			dlog.Tracef(c, "Intercept DNS %s to %s", id, pipeId.DestinationAddr())
			from, to := tunnel.NewPipe(pipeId, s.session.SessionId)
			tunnel.NewDialerTTL(to, func() {}, dnsConnTTL).Start(c)
			return from, nil
		}
		dlog.Debugf(c, "Opening tunnel for id %s", id)
		ct, err := s.managerClient.Tunnel(c)
		if err != nil {
			return nil, err
		}
		tc := client.GetConfig(c).Timeouts
		return tunnel.NewClientStream(c, ct, id, s.session.SessionId, tc.Get(client.TimeoutRoundtripLatency), tc.Get(client.TimeoutEndpointDial))
	}
}
