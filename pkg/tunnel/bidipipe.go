package tunnel

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/datawire/dlib/dlog"
)

type bidiPipe struct {
	a       Stream
	b       Stream
	name    string
	counter *int32
	done    chan struct{}

	probes *BidiPipeProbes
}

type BidiPipeProbes struct {
	BytesProbeA, BytesProbeB chan uint64
}

// NewBidiPipe creates a bidirectional pipe between the two given streams.
func NewBidiPipe(a, b Stream, name string, counter *int32, probes *BidiPipeProbes) Endpoint {
	if probes == nil {
		probes = &BidiPipeProbes{}
	}

	return &bidiPipe{
		a:       a,
		b:       b,
		name:    name,
		counter: counter,
		done:    make(chan struct{}),

		probes: probes,
	}
}

// Start starts the dispatching of messages in both directions between the streams. It
// closes the Done() channel when the streams are closed or the context is cancelled.
func (p *bidiPipe) Start(ctx context.Context) {
	go func() {
		defer func() {
			close(p.done)
			atomic.AddInt32(p.counter, -1)
			dlog.Debugf(ctx, "   FWD disconnect %s", p.name)
		}()
		wg := sync.WaitGroup{}
		wg.Add(2)
		dlog.Debugf(ctx, "   FWD connect %s", p.name)
		atomic.AddInt32(p.counter, 1)
		// p.pm collects metrics only for one stream (since the same data is going through both streams)
		go p.doPipe(ctx, p.a, p.b, &wg, nil, nil)
		go p.doPipe(ctx, p.b, p.a, &wg, nil, nil)
		wg.Wait()
	}()
}

func (p *bidiPipe) Done() <-chan struct{} {
	return p.done
}

// doPipe reads from a and writes to b.
func (p *bidiPipe) doPipe(
	ctx context.Context, a, b Stream, wg *sync.WaitGroup,
	readBytesProbe, writeBytesProbe chan uint64,
) {
	defer wg.Done()
	wrCh := make(chan Message, 50)
	defer close(wrCh)
	wg.Add(1)
	WriteLoop(ctx, b, wrCh, wg, writeBytesProbe)
	rdCh, errCh := ReadLoop(ctx, a, readBytesProbe)
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-errCh:
			if ok {
				dlog.Error(ctx, err)
			}
		case m, ok := <-rdCh:
			if !ok {
				return
			}
			select {
			case <-ctx.Done():
				return
			case wrCh <- m:
			}
		}
	}
}
