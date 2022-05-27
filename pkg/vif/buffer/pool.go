package buffer

import "sync"

const defaultMTU = 1500
const maxIPHeader = 40

// A Pool is a specialized sync.Pool for Data. The Data struct is platform specific
type Pool struct {
	pool sync.Pool
	MTU  int
}

// Get returns an entry from the pool if the requested size is less than or equal to
// the pool's MTU. For sizes larger than the MTU, a new entry is allocated and returned,
// but it is not pooled.
func (p *Pool) Get(size int) *Data {
	if size <= p.MTU+maxIPHeader {
		d := p.pool.Get().(*Data)
		d.setLength(size)
		return d
	}
	return NewData(size)
}

// Put will put entries with a size less than or equal to MTU back to the pool.
func (p *Pool) Put(b *Data) {
	if cap(b.Buf()) == p.MTU+maxIPHeader {
		p.pool.Put(b)
	}
}

var DataPool = &Pool{
	pool: sync.Pool{
		New: func() interface{} {
			return NewData(defaultMTU + maxIPHeader)
		}},
	MTU: defaultMTU,
}
