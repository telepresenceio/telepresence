package buffer

const PrefixLen = 4

// Data on a macOS consists of two slices that share the same underlying byte array. The
// raw data points to the beginning of the array and the buf points PrefixLen into the array.
// All data manipulation is then done using the buf, except reads/writes to the tun device which
// uses the raw. This setup enables the read/write to receive and write the required 4-byte
// header that macOS TUN socket uses without copying data.
type Data struct {
	buf []byte
	raw []byte
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
	c.buf = c.buf[:n]
	c.raw = c.raw[:n+PrefixLen]
	copy(c.raw, s.raw)
	return c
}

// setLength sets the length of this buffer. This will change the slice that Buf and Raw returns
func (b *Data) setLength(l int) {
	b.buf = b.buf[:l]
	b.raw = b.raw[:l+PrefixLen]
}

// Raw returns this Data's raw buffer. This is the buffer that should be used by the tun.Device
// ReadPacket and WritePacket methods. It uses the same underlying byte array as Buf but might be
// offset before Buf to allow for leading bytes that are provided before the IP header.
func (b *Data) Raw() []byte {
	return b.raw
}

func NewData(sz int) *Data {
	raw := make([]byte, PrefixLen+sz)
	return &Data{buf: raw[PrefixLen:], raw: raw}
}

func (d *Data) Resize(size int) {
	if size <= cap(d.buf) {
		d.buf = d.buf[:size]
		d.raw = d.raw[:size+PrefixLen]
	} else {
		d.raw = make([]byte, size+PrefixLen)
		d.buf = d.raw[PrefixLen:]
	}
}
