package rootd

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
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
		if p == ipproto.UDP {
			if s.isForDNS(id.Destination(), id.DestinationPort()) {
				pipeId := tunnel.NewConnID(p, id.Source(), s.dnsLocalAddr.IP, id.SourcePort(), uint16(s.dnsLocalAddr.Port))
				dlog.Tracef(c, "Intercept DNS %s to %s", id, pipeId.DestinationAddr())
				from, to := tunnel.NewPipe(pipeId, s.session.SessionId)
				tunnel.NewDialerTTL(to, func() {}, dnsConnTTL, nil, nil).Start(c)
				return from, nil
			}
			if id.SourcePort() == 53 {
				srcIp := id.Source()
				for _, sn := range s.clusterSubnets {
					if sn.Contains(srcIp) && !srcIp.Equal(sn.IP.Mask(sn.Mask)) {
						// This is call was made by the cluster's DNS service. Typically, seen when
						// running a Kind cluster locally. Letting it through causes recursion and
						// poor performance.
						return nil, errors.New("refusing recursive DNS dispatch")
					}
				}
			}
		}
		var err error
		var ct tunnel.GRPCClientStream
		if agentClient := s.getAgentClient(id.Destination()); agentClient != nil {
			dlog.Debugf(c, "Opening traffic-agent tunnel for id %s", id)
			ct, err = agentClient.Tunnel(c)
		} else {
			dlog.Debugf(c, "Opening traffic-manager tunnel for id %s", id)
			ct, err = s.managerClient.Tunnel(c)
		}
		if err != nil {
			return nil, err
		}
		tc := client.GetConfig(c).Timeouts()
		return tunnel.NewClientStream(c, ct, id, s.session.SessionId, tc.Get(client.TimeoutRoundtripLatency), tc.Get(client.TimeoutEndpointDial))
	}
}

func (s *Session) getAgentClient(ip net.IP) (pvd tunnel.Provider) {
	if s.agentClients != nil {
		pvd = s.agentClients.GetClient(ip)
	}
	return
}
