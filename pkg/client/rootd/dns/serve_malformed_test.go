package dns

import (
	"context"
	"net"
	"testing"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
)

type recordingWriter struct {
	dns.ResponseWriter
	msgs []*dns.Msg
}

func (w *recordingWriter) WriteMsg(m *dns.Msg) error {
	w.msgs = append(w.msgs, m)
	return nil
}

func (w *recordingWriter) Close() error { return nil }

func (w *recordingWriter) RemoteAddr() net.Addr { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)} }

// The local DNS listener address is chosen at random and is not discoverable from a test, so this is a unit test.
func TestServeDNS_messageWithoutQuestion(t *testing.T) {
	s := NewServer(nil, "ns", nil)
	s.ctx = t.Context()
	w := &recordingWriter{}
	require.NotPanics(t, func() { s.ServeDNS(w, &dns.Msg{MsgHdr: dns.MsgHdr{Id: 7}}) })
	require.Len(t, w.msgs, 1)
	require.Equal(t, dns.RcodeFormatError, w.msgs[0].Rcode)
}

// A cluster test cannot make a lookup panic.
func TestResolveThruCache_panicLeavesNoEntry(t *testing.T) {
	s := NewServer(nil, "ns", func(context.Context, *dns.Question) (dnsproxy.RRs, int, error) {
		panic("boom")
	})
	s.ctx = t.Context()
	s.IncludeSuffixes = []string{".example"}
	q := &dns.Question{Name: "a.example.", Qtype: dns.TypeA, Qclass: dns.ClassINET}
	require.Panics(t, func() { _, _, _ = s.resolveThruCache(q) })
	_, found := s.cache.Load(cacheKey{name: q.Name, qType: q.Qtype})
	require.False(t, found)
}
