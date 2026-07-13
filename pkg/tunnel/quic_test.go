package tunnel

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net/netip"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func TestQuicFrame_RoundTrip(t *testing.T) {
	for _, payload := range [][]byte{
		[]byte("hello world"),
		{},
		bytes.Repeat([]byte{0xaa}, 65536),
	} {
		buf := &bytes.Buffer{}
		require.NoError(t, writeFrame(buf, payload))
		got, err := readFrame(buf)
		require.NoError(t, err)
		assert.Equal(t, payload, got)
	}
}

func TestQuicFrame_CleanEOFAtBoundary(t *testing.T) {
	_, err := readFrame(bytes.NewReader(nil))
	assert.ErrorIs(t, err, io.EOF)
}

func TestQuicFrame_MidFrameEOFIsNotEOF(t *testing.T) {
	// Only 2 of the 4 length-prefix bytes are present.
	_, err := readFrame(bytes.NewReader([]byte{0, 0}))
	require.Error(t, err)
	assert.False(t, errors.Is(err, io.EOF))
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)

	// The length prefix is intact but the payload is truncated.
	lb := make([]byte, 4, 9)
	binary.BigEndian.PutUint32(lb, 10)
	_, err = readFrame(bytes.NewReader(append(lb, []byte("short")...)))
	require.Error(t, err)
	assert.False(t, errors.Is(err, io.EOF))
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestQuicFrame_OversizeRejected(t *testing.T) {
	lb := make([]byte, 4)
	binary.BigEndian.PutUint32(lb, maxFrameSize+1)
	_, err := readFrame(bytes.NewReader(lb))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")

	err = writeFrame(&bytes.Buffer{}, make([]byte, maxFrameSize+1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum")
}

// generateQuicTLSConfig returns a bare-bones self-signed server TLS config for use with quic-go,
// configured for QuicALPN.
func generateQuicTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := x509.Certificate{SerialNumber: big.NewInt(1)}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, priv.Public(), priv)
	require.NoError(t, err)
	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{certDER},
			PrivateKey:  priv,
		}},
		NextProtos: []string{QuicALPN},
	}
}

func TestQuicE2E(t *testing.T) {
	ctx, cancel := testContext(t, 10*time.Second)
	defer cancel()

	ln, err := quic.ListenAddr("127.0.0.1:0", generateQuicTLSConfig(t), nil)
	require.NoError(t, err)
	defer ln.Close()

	id := NewConnID(types.ProtoTCP,
		netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 1001),
		netip.AddrPortFrom(netip.AddrFrom4([4]byte{192, 168, 0, 1}), 8080))
	si := SessionID(uuid.New().String())

	serverStreamCh := make(chan Stream, 1)
	serverErrCh := make(chan error, 1)
	go func() {
		conn, err := ln.Accept(ctx)
		if err != nil {
			serverErrCh <- err
			return
		}
		qs, err := conn.AcceptStream(ctx)
		if err != nil {
			serverErrCh <- err
			return
		}
		s, err := NewServerStream(ctx, ManagerToClient, NewQuicServerStream(qs))
		if err != nil {
			serverErrCh <- err
			return
		}
		serverStreamCh <- s
	}()

	clientTLSConf := &tls.Config{
		InsecureSkipVerify: true, // no CA to verify against in this test
		NextProtos:         []string{QuicALPN},
	}
	conn, err := quic.DialAddr(ctx, ln.Addr().String(), clientTLSConf, nil)
	require.NoError(t, err)
	defer func() {
		_ = conn.CloseWithError(0, "")
	}()

	provider := NewQuicProvider(conn)
	cs, err := provider.Tunnel(ctx)
	require.NoError(t, err)

	client, err := NewClientStream(ctx, ClientToManager, cs, id, si, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, Version, client.PeerVersion())

	var server Stream
	select {
	case server = <-serverStreamCh:
	case err := <-serverErrCh:
		t.Fatalf("server side failed: %v", err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for server stream")
	}
	assert.Equal(t, id, server.ID())
	assert.Equal(t, si, server.SessionID())
	assert.Equal(t, Version, server.PeerVersion())

	payload := []byte("ping")
	require.NoError(t, client.Send(ctx, NewMessage(Normal, payload)))
	m, err := server.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, payload, m.Payload())

	reply := []byte("pong")
	require.NoError(t, server.Send(ctx, NewMessage(Normal, reply)))
	m, err = client.Receive(ctx)
	require.NoError(t, err)
	assert.Equal(t, reply, m.Payload())

	require.NoError(t, client.CloseSend(ctx))
	_, err = server.Receive(ctx)
	assert.ErrorIs(t, err, io.EOF)
}
