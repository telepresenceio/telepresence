package quic

import (
	"sync"
	"sync/atomic"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// TunnelwireQuicHardening attacks the QUIC listener of the traffic-manager and the
// QUIC forwarder with the credentials of an ordinary session.
type TunnelwireQuicHardening struct {
	rt.Suite
}

func init() {
	rt.Register(&TunnelwireQuicHardening{}, rt.InArea("quic"), rt.NeedsManager(managers.QuicNodePort()))
}

// tunnelwireFlood is the number of concurrent QUIC connections from one host in the flood
// test. It is well above the forwarder's per-address flow limit.
const tunnelwireFlood = 100

// tunnelwireFlowLimitPerIP is the largest number of flows the forwarder keeps for one
// source address.
const tunnelwireFlowLimitPerIP = 64

// attackStreams opens one stream per element of data on a fresh connection, writes the
// element, and reports per stream whether the manager closed it within wait.
func (s *TunnelwireQuicHardening) attackStreams(p *tunnelwirePeer, data [][]byte, wait time.Duration) []bool {
	conn, err := p.dial(s.Ctx())
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = conn.CloseWithError(0, "") })
	closed := make([]bool, len(data))
	var wg sync.WaitGroup
	for i, d := range data {
		st, err := conn.OpenStreamSync(s.Ctx())
		s.Require().NoError(err)
		_, err = st.Write(d)
		s.Require().NoError(err)
		wg.Add(1)
		go func(i int, st *quicgo.Stream) {
			defer wg.Done()
			closed[i] = tunnelwireStreamClosed(st, wait)
		}(i, st)
	}
	wg.Wait()
	return closed
}

func (s *TunnelwireQuicHardening) requireAllClosed(closed []bool) {
	for i, c := range closed {
		s.True(c, "the manager did not close stream %d", i)
	}
}

func (s *TunnelwireQuicHardening) requireManagerIntact(before map[string]rt.TunnelwireContainerState) {
	time.Sleep(tunnelwireSettle)
	s.Equal(before, tunnelwireManagerStates(s.T(), s.Ctx(), s.R()), "the traffic-manager restarted or stopped being Ready")
}

// Test_FrameBodyNeverSentStreamClosed announces a frame of the maximum size on several
// streams without sending its body. The manager must close each stream within the frame
// body deadline and stay up.
func (s *TunnelwireQuicHardening) Test_FrameBodyNeverSentStreamClosed() {
	t := s.T()
	s.Manager()
	p := tunnelwireArrive(t, s.Ctx(), s.R(), s.AppNamespace())
	tunnelwireAwaitManagerReady(t, s.Ctx(), s.R())
	before := tunnelwireManagerStates(t, s.Ctx(), s.R())

	header := []byte{0x00, 0x40, 0x00, 0x00} // 4 MiB
	closed := s.attackStreams(p, [][]byte{header, header, header, header, header, header, header, header}, tunnelwireReadTimeout)
	s.requireAllClosed(closed)
	s.requireManagerIntact(before)
}

// Test_TruncatedFirstMessageStreamClosed sends first tunnel messages whose declared
// ConnID length overruns the message. The manager must close each stream and stay up.
func (s *TunnelwireQuicHardening) Test_TruncatedFirstMessageStreamClosed() {
	t := s.T()
	s.Manager()
	p := tunnelwireArrive(t, s.Ctx(), s.R(), s.AppNamespace())
	tunnelwireAwaitManagerReady(t, s.Ctx(), s.R())
	before := tunnelwireManagerStates(t, s.Ctx(), s.R())

	var frames [][]byte
	for _, m := range rt.TunnelwireTruncatedIDMessages() {
		frames = append(frames, tunnelwireFrame(m))
	}
	s.requireAllClosed(s.attackStreams(p, frames, tunnelwireReadTimeout))
	s.requireManagerIntact(before)
}

// Test_BadConnIDLengthStreamClosed sends first tunnel messages from the session that
// owns the connection's certificate, carrying a ConnID of an invalid length. The manager
// must close each stream and stay up.
func (s *TunnelwireQuicHardening) Test_BadConnIDLengthStreamClosed() {
	t := s.T()
	s.Manager()
	p := tunnelwireArrive(t, s.Ctx(), s.R(), s.AppNamespace())
	tunnelwireAwaitManagerReady(t, s.Ctx(), s.R())
	before := tunnelwireManagerStates(t, s.Ctx(), s.R())

	var frames [][]byte
	for _, m := range rt.TunnelwireBadConnIDMessages(p.session.GetSessionId()) {
		frames = append(frames, tunnelwireFrame(m))
	}
	s.requireAllClosed(s.attackStreams(p, frames, tunnelwireReadTimeout))
	s.requireManagerIntact(before)
}

// Test_FlowFloodFromOneHostIsCapped opens many concurrent QUIC connections from this
// host through the forwarder. The forwarder must refuse the flows beyond its per-address
// limit, so no more than that many handshakes complete. The forwarder is replaced
// afterwards to clear its flow table.
func (s *TunnelwireQuicHardening) Test_FlowFloodFromOneHostIsCapped() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	s.Manager()
	p := tunnelwireArrive(t, ctx, r, s.AppNamespace())
	t.Cleanup(func() { deleteQuicForwarderPods(t, ctx, r) })

	var established atomic.Int32
	var wg sync.WaitGroup
	conns := make(chan *quicgo.Conn, tunnelwireFlood)
	for range tunnelwireFlood {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if conn, err := p.dial(ctx); err == nil {
				established.Add(1)
				conns <- conn
			}
		}()
	}
	wg.Wait()
	close(conns)
	for conn := range conns {
		t.Cleanup(func() { _ = conn.CloseWithError(0, "") })
	}
	s.LessOrEqual(int(established.Load()), tunnelwireFlowLimitPerIP,
		"%d of %d connections from one host completed a handshake", established.Load(), tunnelwireFlood)
}
