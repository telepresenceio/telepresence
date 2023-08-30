package tunnel

import (
	"sync/atomic"
)

type CounterProbe struct {
	name  string
	value uint64
}

func NewCounterProbe(name string) *CounterProbe {
	return &CounterProbe{name: name}
}

func (p *CounterProbe) Increment(v uint64) {
	atomic.AddUint64(&p.value, v)
}

func (p *CounterProbe) GetName() string {
	return p.name
}

func (p *CounterProbe) GetValue() uint64 {
	return atomic.LoadUint64(&p.value)
}
