package tunnel

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"net"
	"net/netip"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func idOfLength(n int, proto types.Proto) ConnID {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i + 1)
	}
	if n > 0 {
		b[n-1] = byte(proto)
	}
	return ConnID(b)
}

func TestConnID_Valid(t *testing.T) {
	for n := 0; n <= 40; n++ {
		for _, p := range []types.Proto{types.ProtoTCP, types.ProtoUDP, types.ProtoICMP, 0} {
			want := (n == 13 || n == 37) && (p == types.ProtoTCP || p == types.ProtoUDP)
			assert.Equal(t, want, idOfLength(n, p).Valid(), "len %d proto %d", n, p)
		}
	}
}

func TestConnID_AccessorsNeverPanic(t *testing.T) {
	for n := 0; n <= 40; n++ {
		for _, p := range []types.Proto{types.ProtoTCP, types.ProtoUDP, 99} {
			id := idOfLength(n, p)
			require.NotPanics(t, func() {
				_ = id.Source()
				_ = id.SourceAddr()
				_ = id.SourcePort()
				_ = id.Destination()
				_ = id.DestinationAddr()
				_ = id.DestinationPort()
				_ = id.Protocol()
				_ = id.IsSourceIPv4()
				_ = id.IsDestinationIPv4()
				_ = id.SourceProtocolString()
				_ = id.DestinationProtocolString()
				_ = id.SourceNetwork()
				_ = id.DestinationNetwork()
				_ = id.Reply()
				_ = id.ReplyString()
				_ = id.String()
			}, "len %d proto %d", n, p)
			if n != 13 && n != 37 {
				assert.False(t, id.Destination().IsValid(), "len %d", n)
			}
		}
	}
}

func connectPayload(id ConnID, session string) []byte {
	return StreamInfoMessage(id, SessionID(session), time.Second, time.Second).(msg)
}

func TestSetConnectInfo_IDLengths(t *testing.T) {
	for n := 0; n <= 40; n++ {
		id := idOfLength(n, types.ProtoTCP)
		s := &stream{}
		var err error
		require.NotPanics(t, func() { err = setConnectInfo(msg(connectPayload(id, "sid")), s) }, "len %d", n)
		if n == 13 || n == 37 {
			require.NoError(t, err, "len %d", n)
			assert.Equal(t, id, s.id)
			assert.Equal(t, SessionID("sid"), s.sessionID)
		} else {
			assert.ErrorIs(t, err, errMalformedConnect, "len %d", n)
		}
	}
	for _, p := range []types.Proto{0, types.ProtoICMP, 255} {
		assert.ErrorIs(t, setConnectInfo(msg(connectPayload(idOfLength(13, p), "sid")), &stream{}), errMalformedConnect)
	}
}

func TestSetConnectInfo_Truncated(t *testing.T) {
	full := connectPayload(idOfLength(13, types.ProtoTCP), "session")
	require.NoError(t, setConnectInfo(msg(full), &stream{}))
	for n := 1; n < len(full); n++ {
		var err error
		require.NotPanics(t, func() { err = setConnectInfo(msg(full[:n]), &stream{}) }, "cut at %d", n)
		assert.Error(t, err, "cut at %d", n)
	}
}

func TestSetConnectInfo_LengthOverrun(t *testing.T) {
	varint := func(v uint64) []byte {
		return binary.AppendUvarint(nil, v)
	}
	head := make([]byte, 0, 32)
	head = append(head, byte(streamInfo))
	head = append(head, varint(uint64(Version))...)
	head = append(head, varint(1)...)
	head = append(head, varint(1)...)
	id := []byte(idOfLength(13, types.ProtoTCP))

	// The declared length exceeds what follows by up to the size of the prefix.
	for _, over := range []int{1, 2, 3, 200, 1 << 20} {
		for _, field := range []string{"id", "session"} {
			m := append([]byte{}, head...)
			if field == "id" {
				m = append(m, varint(uint64(len(id)+over))...)
				m = append(m, id...)
			} else {
				m = append(m, varint(uint64(len(id)))...)
				m = append(m, id...)
				m = append(m, varint(uint64(3+over))...)
				m = append(m, "abc"...)
			}
			var err error
			require.NotPanics(t, func() { err = setConnectInfo(msg(m), &stream{}) }, "%s over %d", field, over)
			assert.ErrorIs(t, err, errMalformedConnect, "%s over %d", field, over)
		}
	}

	// Maximal uvarint lengths.
	m := append([]byte{}, head...)
	m = append(m, varint(^uint64(0))...)
	require.NotPanics(t, func() { assert.Error(t, setConnectInfo(msg(m), &stream{})) })
}

func TestSetConnectInfo_RandomNeverPanics(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	valid := connectPayload(idOfLength(13, types.ProtoUDP), "session")
	for i := 0; i < 20000; i++ {
		var pl []byte
		if i%2 == 0 {
			pl = make([]byte, 1+rnd.Intn(48))
			rnd.Read(pl)
			pl[0] = byte(streamInfo)
		} else {
			pl = append([]byte{}, valid...)
			pl = pl[:1+rnd.Intn(len(pl))]
			for k := 0; k < 1+rnd.Intn(3); k++ {
				pl[rnd.Intn(len(pl))] = byte(rnd.Intn(256))
			}
		}
		s := &stream{}
		require.NotPanics(t, func() {
			if err := setConnectInfo(msg(pl), s); err == nil {
				assert.True(t, s.id.Valid())
			}
		}, "%x", pl)
	}
}

type countingProvider struct {
	calls atomic.Int32
}

func (p *countingProvider) Tunnel(context.Context, ...grpc.CallOption) (GRPCClientStream, error) {
	p.calls.Add(1)
	return nil, errors.New("no tunnel")
}

func runDialLoop(t *testing.T, ids []ConnID, allowed DialAllowed) *countingProvider {
	t.Helper()
	requests := make(chan *rpc.DialRequest, len(ids))
	for _, id := range ids {
		requests <- &rpc.DialRequest{ConnId: []byte(id)}
	}
	close(requests)
	p := &countingProvider{}
	require.NotPanics(t, func() {
		require.NoError(t, DialWaitLoop(context.Background(), AgentToClient, p, &dialRequestStream{requests: requests}, "session", allowed, nil))
	})
	return p
}

func TestDialWaitLoop_MalformedIDsAreDropped(t *testing.T) {
	var ids []ConnID
	for n := 0; n <= 40; n++ {
		if n != 13 && n != 37 {
			ids = append(ids, idOfLength(n, types.ProtoTCP))
		}
	}
	ids = append(ids, idOfLength(13, types.ProtoICMP), idOfLength(37, 0))
	var asked atomic.Int32
	p := runDialLoop(t, ids, func(ConnID) bool { asked.Add(1); return true })
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, asked.Load())
	assert.Zero(t, p.calls.Load())
}

func TestDialWaitLoop_RefusesWhenNotAllowed(t *testing.T) {
	ids := []ConnID{idOfLength(13, types.ProtoTCP), idOfLength(37, types.ProtoUDP)}
	for name, allowed := range map[string]DialAllowed{
		"nil":   nil,
		"false": func(ConnID) bool { return false },
	} {
		t.Run(name, func(t *testing.T) {
			p := runDialLoop(t, ids, allowed)
			// Each refused request is answered with a reject, which needs a tunnel; none is served.
			require.Eventually(t, func() bool { return p.calls.Load() == int32(len(ids)) }, 5*time.Second, 10*time.Millisecond)
		})
	}
}

func TestDialWaitLoop_ServesAllowed(t *testing.T) {
	target := NewConnID(types.ProtoTCP, netip.MustParseAddrPort("10.0.0.1:1000"), netip.MustParseAddrPort("127.0.0.1:8080"))
	other := NewConnID(types.ProtoTCP, netip.MustParseAddrPort("10.0.0.1:1000"), netip.MustParseAddrPort("169.254.169.254:80"))
	var served []ConnID
	p := runDialLoop(t, []ConnID{target, other}, func(id ConnID) bool {
		ok := id == target
		if ok {
			served = append(served, id)
		}
		return ok
	})
	require.Eventually(t, func() bool { return p.calls.Load() == 2 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []ConnID{target}, served)
}

func totalAlloc() uint64 {
	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	return ms.TotalAlloc
}

func TestReadFrame_AnnouncedSizeCommitsNoMemory(t *testing.T) {
	hdr := binary.BigEndian.AppendUint32(nil, maxFrameSize)
	before := totalAlloc()
	_, err := readFrame(bytes.NewReader(hdr))
	after := totalAlloc()
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.Less(t, after-before, uint64(1<<20))
}

func TestReadFrame_StalledBodyTimesOut(t *testing.T) {
	old := frameBodyTimeout
	frameBodyTimeout = 100 * time.Millisecond
	t.Cleanup(func() { frameBodyTimeout = old })

	c, s := net.Pipe()
	defer c.Close()
	defer s.Close()
	go func() {
		_, _ = c.Write(binary.BigEndian.AppendUint32(nil, maxFrameSize))
	}()
	before := totalAlloc()
	start := time.Now()
	_, err := readFrame(s)
	after := totalAlloc()
	require.Error(t, err)
	var ne net.Error
	require.ErrorAs(t, err, &ne)
	assert.True(t, ne.Timeout())
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Less(t, after-before, uint64(1<<20))
}

func TestReadFrame_DeadlineClearedAfterFrame(t *testing.T) {
	old := frameBodyTimeout
	frameBodyTimeout = 100 * time.Millisecond
	t.Cleanup(func() { frameBodyTimeout = old })

	c, s := net.Pipe()
	defer c.Close()
	defer s.Close()
	go func() {
		_ = writeFrame(c, []byte("hello"))
		time.Sleep(300 * time.Millisecond)
		_ = writeFrame(c, []byte("world"))
	}()
	b, err := readFrame(s)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(b))
	// Waiting for the next header is not subject to the body deadline.
	b, err = readFrame(s)
	require.NoError(t, err)
	assert.Equal(t, "world", string(b))
}

func TestReadFrame_LargeFrameRoundTrip(t *testing.T) {
	payload := make([]byte, 3*1024*1024+17)
	rand.New(rand.NewSource(2)).Read(payload)
	buf := &bytes.Buffer{}
	require.NoError(t, writeFrame(buf, payload))
	got, err := readFrame(buf)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(payload, got))
}

func TestReadFrame_SmallFrameAllocatesHeaderAndBody(t *testing.T) {
	buf := &bytes.Buffer{}
	require.NoError(t, writeFrame(buf, make([]byte, 1024)))
	frame := buf.Bytes()
	r := bytes.NewReader(frame)
	allocs := testing.AllocsPerRun(100, func() {
		r.Reset(frame)
		_, _ = readFrame(r)
	})
	assert.LessOrEqual(t, allocs, 2.0) // the length header and the body
}
