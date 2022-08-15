//go:build !darwin
// +build !darwin

package buffer

type Data struct {
	buf []byte
}

// Buf returns this Data's buffer. This is the buffer that should be used everywhere
// except for the tun.Device ReadPacket and WritePacket methods.
func (d *Data) Buf() []byte {
	return d.buf
}

// Copy copies n bytes from the given Data buffer into a new Data and returns it.
func (d *Data) Copy(n int) *Data {
	c := NewData(n)
	copy(c.buf, d.buf)
	return c
}

// Raw returns this Data's raw buffer. This is the buffer that should be used by the tun.Device
// ReadPacket and WritePacket methods. It uses the same underlying byte array as Buf but might be
// offset before Buf to allow for leading bytes that are provided before the IP header.
func (d *Data) Raw() []byte {
	return d.buf
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
