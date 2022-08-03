package vif

import (
	"context"
	"sync"
	"sync/atomic"

	"gvisor.dev/gvisor/pkg/bufferv2"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/buffer"
)

type endpoint struct {
	*channel.Endpoint
	ctx     context.Context
	wg      sync.WaitGroup
	dev     *Device
	closing *int32
}

const defaultDevMtu = 1500

// Queue length for outbound packet, arriving at fd side for read. Overflow
// causes packet drops. gVisor implementation-specific.
const defaultDevOutQueueLen = 1024

var _ stack.LinkEndpoint = (*endpoint)(nil)

func NewEndpoint(ctx context.Context, dev *Device, closing *int32) stack.LinkEndpoint {
	return &endpoint{
		Endpoint: channel.New(defaultDevOutQueueLen, defaultDevMtu, ""),
		ctx:      ctx,
		dev:      dev,
		closing:  closing,
	}
}

func (d *endpoint) Attach(dp stack.NetworkDispatcher) {
	d.Endpoint.Attach(dp)
	if dp == nil {
		// Stack is closing
		return
	}
	d.wg.Add(2)
	dlog.Info(d.ctx, "Starting Endpoint")
	ctx, cancel := context.WithCancel(d.ctx)
	go d.tunToDispatch(cancel)
	go d.dispatchToTun(ctx)
}

func (d *endpoint) Wait() {
	d.wg.Wait()
	dlog.Info(d.ctx, "Endpoint done")
}

func (d *endpoint) tunToDispatch(cancel context.CancelFunc) {
	defer func() {
		cancel()
		d.wg.Done()
	}()
	buf := buffer.NewData(0x10000)
	data := buf.Buf()
	for atomic.LoadInt32(d.closing) < 2 {
		n, err := d.dev.ReadPacket(buf)
		if err != nil {
			if d.ctx.Err() == nil && atomic.LoadInt32(d.closing) == 2 {
				dlog.Errorf(d.ctx, "read packet error: %v", err)
			}
			return
		}
		if n == 0 {
			continue
		}

		var ipv tcpip.NetworkProtocolNumber
		switch header.IPVersion(data) {
		case header.IPv4Version:
			ipv = header.IPv4ProtocolNumber
		case header.IPv6Version:
			ipv = header.IPv6ProtocolNumber
		default:
			continue
		}

		pb := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: bufferv2.MakeWithData(data[:n]),
		})
		d.InjectInbound(ipv, pb)
		pb.DecRef()
	}
}

func (d *endpoint) dispatchToTun(ctx context.Context) {
	defer d.wg.Done()
	buf := buffer.NewData(0x10000)
	for {
		pb := d.ReadContext(ctx)
		if pb == nil {
			break
		}
		buf.Resize(pb.Size())
		b := buf.Buf()
		for _, s := range pb.AsSlices() {
			copy(b, s)
			b = b[len(s):]
		}
		pb.DecRef()
		if _, err := d.dev.WritePacket(buf, 0); err != nil {
			dlog.Errorf(ctx, "WritePacket failed: %v", err)
		}
	}
}
