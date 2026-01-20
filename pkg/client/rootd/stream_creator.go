package rootd

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/puzpuzpuz/xsync/v4"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

const dnsConnTTL = 5 * time.Second

func (s *session) isForDNS(ip netip.Addr, port uint16) bool {
	return s.vifDNS.Addr() == ip && s.vifDNS.Port() == port
}

// checkRecursion checks that the given IP is not contained in any of the subnets
// that the VIF is configured with. When that's the case, the VIF is somehow receiving
// requests that originate from the cluster and dispatching it leads to infinite recursion.
func checkRecursion(p types.Proto, ip netip.Addr, sn netip.Prefix) (err error) {
	if sn.Contains(ip) && ip != sn.Masked().Addr() {
		err = fmt.Errorf("refusing recursive %s %s dispatch from pod subnet %s", p, ip, sn)
	}
	return err
}

type recursiveBlock struct {
	start time.Time
	count int
	timer *time.Timer
}

func (s *session) streamCreator() tunnel.StreamCreator {
	var recursionBlockMap *xsync.Map[netip.AddrPort, recursiveBlock]
	routing := client.GetConfig(s).Routing()
	recursionBlockDuration := routing.RecursionBlockDuration
	recursionBlockThreads := routing.RecursionBlockTreads
	if recursionBlockDuration != 0 {
		recursionBlockMap = xsync.NewMap[netip.AddrPort, recursiveBlock]()
	}

	return func(c context.Context, id tunnel.ConnID) (tunnel.Stream, error) {
		p := id.Protocol()
		srcIp := id.SourceAddr()
		for _, podSn := range s.podSubnets {
			if err := checkRecursion(p, srcIp, podSn); err != nil {
				return nil, err
			}
		}

		destAddr := id.DestinationAddr()
		if p == types.ProtoUDP {
			if s.isForDNS(destAddr, id.DestinationPort()) {
				pipeId := tunnel.NewConnID(p, id.Source(), s.localDNS)
				clog.Tracef(c, "Intercept DNS %s to %s", id, pipeId.Destination())
				from, to := tunnel.NewPipe(pipeId, tunnel.SessionID(s.session.SessionId), tunnel.DnsToTun, tunnel.TunToDNS)
				tunnel.NewDialerTTL(to, func() {}, dnsConnTTL, nil, nil).Start(c)
				return from, nil
			}
		}

		if mp, ok := s.l4PortMap.Load(types.AddrPortProto{
			AddrPort: netip.AddrPortFrom(destAddr, id.DestinationPort()),
			Proto:    id.Protocol(),
		}); ok {
			id = tunnel.NewConnID(id.Protocol(), id.Source(), netip.AddrPortFrom(destAddr, mp))
		}

		if recursionBlockDuration > 0 {
			dst := netip.AddrPortFrom(destAddr, id.DestinationPort())
			block := false
			recursionBlockMap.Compute(dst, func(v recursiveBlock, loaded bool) (recursiveBlock, xsync.ComputeOp) {
				if loaded {
					if time.Since(v.start) < recursionBlockDuration {
						v.count++
						if v.count < recursionBlockThreads {
							return v, xsync.UpdateOp
						}
						block = true
					}
					v.timer.Stop()
					return v, xsync.DeleteOp
				}

				// Ensure deletion in case it's only called once
				v.timer = time.AfterFunc(recursionBlockDuration*3, func() {
					recursionBlockMap.Delete(dst)
				})
				v.start = time.Now()
				return v, xsync.UpdateOp
			})
			if block {
				return nil, fmt.Errorf("refusing recursive dispatch to %s", dst)
			}
		}

		var err error
		var tp tunnel.Provider
		if a, ok := s.getAgentVIP(destAddr); ok {
			// s.agentClients is never nil when agentVIPs are used.
			if a.workload != "" {
				tp = s.agentClients.GetWorkloadClient(a.workload)
				if tp == nil {
					return nil, fmt.Errorf("unable to connect to a traffic-agent for workload %q", a.workload)
				}
				// Replace the virtual IP with the original destination IP. This will ensure that the agent
				// dials the original destination when the tunnel is established.
				id = tunnel.NewConnID(id.Protocol(), id.Source(), netip.AddrPortFrom(a.destinationIP, id.DestinationPort()))
				clog.Debugf(c, "Opening proxy-via %s tunnel for id %s", a.workload, id)
			} else {
				clog.Debugf(c, "Translating proxy-via %s to %s", destAddr, a.destinationIP)
				destAddr = a.destinationIP
				id = tunnel.NewConnID(id.Protocol(), id.Source(), netip.AddrPortFrom(destAddr, id.DestinationPort()))
			}
		}

		if tp == nil {
			tp = s.getAgentClient(destAddr)
			if tp != nil {
				clog.Debugf(c, "Opening traffic-agent tunnel for id %s using agent %s", id, tp)
			} else {
				tp = tunnel.ManagerProvider(s.managerClient())
				clog.Debugf(c, "Opening traffic-manager tunnel for id %s", id)
			}
		}
		ct, err := tp.Tunnel(c)
		if err != nil {
			return nil, err
		}
		if id.Protocol() == types.ProtoTCP {
			s.MarkActivity()
		}

		tc := client.GetConfig(c).Timeouts()
		return tunnel.NewClientStream(
			c, tunnel.TunToClient, ct, id, tunnel.SessionID(s.session.SessionId), tc.Get(client.TimeoutRoundtripLatency), tc.Get(client.TimeoutEndpointDial))
	}
}

func (s *session) getAgentVIP(dest netip.Addr) (a agentVIP, ok bool) {
	if s.virtualIPs != nil {
		a, ok = s.virtualIPs.Load(dest)
	}
	return a, ok
}

func (s *session) getAgentClient(ip netip.Addr) (pvd tunnel.Provider) {
	if s.agentClients != nil {
		pvd = s.agentClients.GetClient(ip)
	}
	return pvd
}
