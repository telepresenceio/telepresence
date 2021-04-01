package buffer

import (
	"sync"
)

const PrefixLen = 4

// Data on a MacOS consists of two slices that share the same underlying byte array. The
// raw data points to the beginning of the array and the buf points PrefixLen into the array.
// All data manipulation is then done using the buf, except reads/writes to the tun device which
// uses the raw. This setup enables the read/write to receive and write the required 4-byte
// header that MacOS TUN socket uses without copying data.
type Data struct {
	buf []byte
	raw []byte
}

// Buf returns this Data's buffer. This is the buffer that should be used everywhere
// except for the tun.Device Read and Write methods.
func (b *Data) Buf() []byte {
	return b.buf
}

// SetLength sets the length of this buffer. This will change the slice that Buf and Raw returns
func (b *Data) SetLength(l int) {
	if l > cap(b.buf) {
		raw := b.raw
		b.raw = make([]byte, l+PrefixLen)
		copy(b.raw, raw)
		b.buf = b.raw[PrefixLen:]
	} else {
		b.buf = b.buf[:l]
		b.raw = b.raw[:l+PrefixLen]
	}
}

// Raw returns this Data's raw buffer. This is the buffer that should by the tun.Device Read and Write
// methods. It uses the same underlying byte array as Buf but might be offset before Buf to allow for
// leading bytes that are provided before the IP header.
func (b *Data) Raw() []byte {
	return b.raw
}

var DataPool = &Pool{
	pool: sync.Pool{
		New: func() interface{} {
			raw := make([]byte, PrefixLen+defaultMTU+maxIPHeader)
			return &Data{buf: raw[PrefixLen:], raw: raw}
		}},
	MTU: defaultMTU,
}
