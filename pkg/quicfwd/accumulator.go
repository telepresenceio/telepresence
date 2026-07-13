package quicfwd

// CryptoAccumulator reassembles a single QUIC connection attempt's Initial-packet
// CRYPTO stream so a caller can retry SNI extraction as each Initial packet of a
// multi-packet ClientHello arrives. ExtractSNI only ever sees one packet at a time and
// therefore cannot recover a ClientHello whose CRYPTO data was split across several
// Initial packets -- confirmed, for this module's quic-go version and a default Go TLS
// client configuration, by the capture tests in capture_test.go, which observed the
// split placing no contiguous offset-0 run in either individual packet. A forwarder
// keeps one CryptoAccumulator per (source address, client DCID) handshake attempt (see
// "The forwarder" in docs/plans/quic-transport/design.md) and calls Feed with each
// Initial packet it sees for that attempt, in arrival order, until ok is true or the
// attempt is abandoned (cap/TTL eviction is the caller's responsibility; this type holds
// no time source and enforces no limit of its own on how much it will accumulate).
//
// A CryptoAccumulator is not safe for concurrent use; callers serialize Feed calls per
// handshake-cache entry, exactly as they must already serialize appends to that entry's
// buffered-datagram list.
type CryptoAccumulator struct {
	segments []cryptoSegment
}

// NewCryptoAccumulator returns an empty accumulator, ready to Feed the first Initial
// packet of a new connection attempt.
func NewCryptoAccumulator() *CryptoAccumulator {
	return &CryptoAccumulator{}
}

// Feed decrypts initialPacket (which must be a QUIC v1 Initial packet -- the same
// requirement ExtractSNI imposes), adds its CRYPTO frames to everything accumulated so
// far, and retries the ClientHello parse against the merged result.
//
// The return semantics mirror ExtractSNI exactly: ok is false with a nil error when the
// accumulated CRYPTO data still doesn't reach the server_name extension -- expected for
// every packet of a multi-packet ClientHello but the last, and the caller should Feed
// the next packet as it arrives (or give up, per its own cap/TTL policy). A non-nil
// error means initialPacket itself is malformed exactly as ExtractSNI defines
// malformed; the accumulator's prior state is left unchanged, since a bad packet is not
// evidence that previously accumulated data was wrong.
func (a *CryptoAccumulator) Feed(initialPacket []byte) (sni string, ok bool, err error) {
	segments, err := decryptInitialCrypto(initialPacket)
	if err != nil {
		return "", false, err
	}
	merged := make([]cryptoSegment, 0, len(a.segments)+len(segments))
	merged = append(merged, a.segments...)
	merged = append(merged, segments...)
	a.segments = merged

	data, ok := reassembleFromZero(a.segments)
	if !ok {
		return "", false, nil
	}
	return parseClientHelloSNI(data)
}
