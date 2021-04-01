package buffer

import (
	"sync"
)

const defaultMTU = 1500
const maxIPHeader = 60

// A Pool is a specialized sync.Pool for Data. The Data struct is platform specific
type Pool struct {
	pool sync.Pool
	MTU  int
}

func (p *Pool) Get(size int) *Data {
	b := p.pool.Get().(*Data)
	b.SetLength(size)
	return b
}

func (p *Pool) Put(b *Data) {
	p.pool.Put(b)
}
