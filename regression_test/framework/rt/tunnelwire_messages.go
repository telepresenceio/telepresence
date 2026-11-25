package rt

import (
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// TunnelwireTruncatedIDMessages returns first tunnel messages whose ConnID length
// prefix equals the number of bytes left in the message, counted including the prefix
// itself. Total sizes are chosen at allocator size classes so that the declared length
// exceeds the capacity of the slice a decoder works on.
func TunnelwireTruncatedIDMessages() []*manager.TunnelMessage {
	const streamInfoCode = 1 // the first byte of a StreamInfo message
	var msgs []*manager.TunnelMessage
	for _, total := range []int{16, 32, 48, 64, 80, 96, 112, 128} {
		b := make([]byte, total)
		b[0] = streamInfoCode
		b[1] = byte(tunnel.Version)
		// bytes 2 and 3 are the zero latency and dial timeout varints
		b[4] = byte(total - 4)
		msgs = append(msgs, &manager.TunnelMessage{Payload: b})
	}
	return msgs
}

// TunnelwireBadConnIDMessages returns well-formed first tunnel messages for sessionID
// whose ConnID has a length other than the 13 and 37 bytes of an IPv4 and IPv6 ID.
func TunnelwireBadConnIDMessages(sessionID string) []*manager.TunnelMessage {
	var msgs []*manager.TunnelMessage
	for _, n := range []int{0, 5, 20, 40} {
		id := tunnel.ConnID(make([]byte, n))
		msgs = append(msgs, tunnel.StreamInfoMessage(id, tunnel.SessionID(sessionID), 0, 0).TunnelMessage())
	}
	return msgs
}
