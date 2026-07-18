package tunnel

import (
	"bytes"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func testUDPConnID() ConnID {
	return NewConnID(types.ProtoUDP,
		netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 1001),
		netip.AddrPortFrom(netip.AddrFrom4([4]byte{192, 168, 0, 1}), 53))
}

func TestDatagram_RoundTrip(t *testing.T) {
	id := testUDPConnID()
	for _, payload := range [][]byte{
		[]byte("hello world"),
		{},
		bytes.Repeat([]byte{0xaa}, 65536), // larger than any real datagram could be; the
		// codec itself imposes no size limit, only the transport does.
	} {
		gotID, gotPayload, err := DecodeDatagram(EncodeDatagram(id, payload))
		require.NoError(t, err)
		assert.Equal(t, id, gotID)
		assert.Equal(t, payload, gotPayload)
	}
}

func TestDatagram_Truncated(t *testing.T) {
	_, _, err := DecodeDatagram(nil)
	require.Error(t, err)

	id := testUDPConnID()
	full := EncodeDatagram(id, []byte("payload"))
	// Cut the buffer short so the declared ConnID length no longer fits.
	_, _, err = DecodeDatagram(full[:2])
	require.Error(t, err)
}
