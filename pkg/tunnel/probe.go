package tunnel

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type CounterProbe struct {
	lock sync.Mutex

	name    string
	channel chan uint64
	timeout time.Duration
	value   atomic.Uint64
}

const (
	probeChannelTimeout    = 100 * time.Millisecond
	probeChannelBufferSize = 1024
)

func NewCounterProbe(name string) *CounterProbe {
	return &CounterProbe{
		lock:    sync.Mutex{},
		name:    name,
		channel: make(chan uint64, probeChannelBufferSize),
		timeout: probeChannelTimeout,
	}
}

func (p *CounterProbe) Increment(v uint64) error {
	select {
	case p.channel <- v:
	case <-time.After(p.timeout):
		return fmt.Errorf("timeout trying to increment probe channel")
	}
	return nil
}

func (p *CounterProbe) RunCollect(ctx context.Context) {
	defer p.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-p.channel:
			if !ok {
				p.Close()
				return
			}
			p.value.Add(b)
		}
	}
}

func (p *CounterProbe) Close() {
	p.lock.Lock()
	defer p.lock.Unlock()
	if p.channel != nil {
		close(p.channel)
		p.channel = nil
	}
}

func (p *CounterProbe) GetName() string {
	return p.name
}

func (p *CounterProbe) GetValue() uint64 {
	return p.value.Load()
}
