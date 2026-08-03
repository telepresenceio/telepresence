package routing

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// udpEchoReplyPrefix is the fixed string the udp-echo test image
// (workloads.UDPEcho) prepends to every datagram it echoes back: a reply is
// always this prefix immediately followed by the exact bytes it received.
const udpEchoReplyPrefix = "Reply from UDP-echo: "

// udpLargePayloadSize is comfortably under the ~9216-byte ceiling some
// platforms impose on a single UDP datagram, proving a payload near that
// limit still round-trips through the TUN whole rather than only a short
// probe message.
const udpLargePayloadSize = 9000

// udpReadyTimeout/udpReadyInterval bound the first exchange's retry: the
// workload's rollout completing does not guarantee its UDP socket is
// already accepting datagrams (no readiness probe covers it), so the first
// write+read can time out waiting on a listener that starts a moment
// later. udpExchangeTimeout bounds each individual write+read, both inside
// that retry and for the one exchange that follows it.
const (
	udpReadyTimeout    = 30 * time.Second
	udpReadyInterval   = 2 * time.Second
	udpExchangeTimeout = 5 * time.Second
)

// UDP proves arbitrary UDP flows round-trip through the TUN over the
// default (grpc) tunnel transport: a raw net.Dial("udp", ...) session
// carries a small and a large datagram to workloads.UDPEcho's Service, each
// echoed back unchanged. Every UDP payload on this transport rides as an
// ordinary tunnel message on the flow's own stream (pkg/tunnel/
// udplistener.go, pkg/tunnel/datagram.go's package comment) -- a different
// path from the quic area's RFC 9221 datagram carriage
// (suites/quic/datagrams.go), which only carries a UDP flow once both ends
// have negotiated QUIC datagram support and the flow never reaches a
// traffic-agent.
type UDP struct {
	rt.Suite
}

func init() {
	rt.Register(&UDP{}, rt.InArea("routing"), rt.NeedsManager(managers.Default))
}

// Test_SmallAndLargeDatagrams connects with the area's normal default
// connection, dials workloads.UDPEcho's Service address directly, and
// round-trips a small message followed by a ~9000-byte one, checking each
// echoed reply against the known "Reply from UDP-echo: " prefix.
func (s *UDP) Test_SmallAndLargeDatagrams() {
	s.Connect()
	wl := s.Workload(workloads.UDPEcho("routing-udp"))
	addr := fmt.Sprintf("%s.%s:%d", wl.SvcName, wl.Namespace, wl.Port)

	conn, err := net.Dial("udp", addr)
	s.Require().NoError(err, "dial udp %s", addr)
	defer conn.Close()

	small := "small routing-udp payload"
	var firstReply string
	// A UDP Dial succeeds whether or not anything is listening yet, so the
	// first exchange retries on a read timeout instead of treating one as
	// a failure.
	s.Eventually(func() bool {
		reply, xerr := udpExchange(conn, small)
		if xerr != nil {
			return false
		}
		firstReply = reply
		return true
	}, udpReadyTimeout, udpReadyInterval, "UDP echo service at %s never became ready", addr)
	s.Equal(udpEchoReplyPrefix+small, firstReply, "small UDP payload should echo back unchanged")

	large := udpFillPayload(udpLargePayloadSize)
	reply, err := udpExchange(conn, large)
	s.Require().NoError(err, "UDP exchange with %s", addr)
	s.Equal(udpEchoReplyPrefix+large, reply, "large UDP payload should echo back unchanged")
}

// udpExchange writes msg on conn and returns the echoed reply.
func udpExchange(conn net.Conn, msg string) (string, error) {
	if err := conn.SetDeadline(time.Now().Add(udpExchangeTimeout)); err != nil {
		return "", err
	}
	if _, err := conn.Write([]byte(msg)); err != nil {
		return "", err
	}
	buf := make([]byte, 0x10000)
	n, err := conn.Read(buf)
	if err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

// udpFillPayload returns a deterministic string of exactly n bytes.
func udpFillPayload(n int) string {
	const unit = "0123456789"
	return strings.Repeat(unit, n/len(unit)+1)[:n]
}
