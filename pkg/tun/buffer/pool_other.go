// +build !darwin

package buffer

import (
	"sync"
)

type Data struct {
	buf []byte
}

// Buf returns this Data's buffer. This is the buffer that should be used everywhere
// except for the tun.Device Read and Write methods.
func (b *Data) Buf() []byte {
	return b.buf
}

// SetLength sets the length of this buffer. This will change the slice that Buf and Raw returns
func (b *Data) SetLength(l int) {
	if l > cap(b.buf) {
		buf := b.buf
		b.buf = make([]byte, l)
		copy(b.buf, buf)
	} else {
		b.buf = b.buf[:l]
	}
}

// Raw returns this Data's raw buffer. This is the buffer that should by the tun.Device Read and Write
// methods. It uses the same underlying byte array as Buf but might be offset before Buf to allow for
// leading bytes that are provided before the IP header.
func (b *Data) Raw() []byte {
	return b.buf
}

var DataPool = &Pool{
	pool: sync.Pool{
		New: func() interface{} {
			return &Data{buf: make([]byte, defaultMTU+maxIPHeader)}
		}},
	MTU: defaultMTU,
}
