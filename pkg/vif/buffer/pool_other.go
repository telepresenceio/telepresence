//go:build !darwin
// +build !darwin

package buffer

type Data struct {
	buf []byte
}

// Buf returns this Data's buffer. This is the buffer that should be used everywhere
// except for the tun.Device ReadPacket and WritePacket methods.
func (b *Data) Buf() []byte {
	return b.buf
}

// Copy copies n bytes from the given Data buffer into a new Data which is obtained from
// the pool, and returns the new data.
func (p *Pool) Copy(s *Data, n int) *Data {
	c := p.Get(n)
	copy(c.buf, s.buf)
	return c
}

// setLength sets the length of this buffer. This will change the slice that Buf and Raw returns
func (b *Data) setLength(l int) {
	b.buf = b.buf[:l]
}

// Raw returns this Data's raw buffer. This is the buffer that should be used by the tun.Device
// ReadPacket and WritePacket methods. It uses the same underlying byte array as Buf but might be
// offset before Buf to allow for leading bytes that are provided before the IP header.
func (b *Data) Raw() []byte {
	return b.buf
}

func NewData(sz int) *Data {
	return &Data{buf: make([]byte, sz)}
}

func (d *Data) Resize(size int) {
	if size <= cap(d.buf) {
		d.buf = d.buf[:size]
	} else {
		d.buf = make([]byte, size)
	}
}
