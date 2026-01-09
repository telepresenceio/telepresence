//go:build !linux

package vif

import (
	"context"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/telepresenceio/clog"
)

// Queue length for outbound packets, arriving at fd side for read. Overflow
// causes packet drops. gVisor implementation-specific.
const defaultDevOutQueueLen = 1024

// This can be fairly large. We're only allocating one such buffer, and the
// reads are non-blocking.
const ioBufferSize = 1 << 22

func (d *device) createLinkEndpoint() (stack.LinkEndpoint, error) {
	return d, nil
}

func (d *device) WaitForDevice() {
	d.wg.Wait()
}

func (d *device) Attach(dp stack.NetworkDispatcher) {
	go func() {
		d.Endpoint.Attach(dp)
		if dp == nil {
			// Stack is closing
			return
		}
		clog.Info(d.ctx, "Starting Endpoint")
		ctx, cancel := context.WithCancel(d.ctx)
		d.wg.Add(2)
		go d.tunToDispatch(cancel)
		d.dispatchToTun(ctx)
	}()
}

func (d *device) tunToDispatch(cancel context.CancelFunc) {
	defer func() {
		cancel()
		d.wg.Done()
	}()
	buf := make([]byte, ioBufferSize)
	skip := d.headerSkip()
	for {
		clog.Trace(d.ctx, "readPacket")
		n, err := d.readPacket(buf)
		if err != nil {
			if d.IsAttached() && d.ctx.Err() == nil {
				clog.Errorf(d.ctx, "read packet error: %v", err)
			}
			return
		}
		if n-skip <= 0 {
			continue
		}
		data := buf[skip:n]

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
			Payload: buffer.MakeWithData(data),
		})

		d.InjectInbound(ipv, pb)
		pb.DecRef()
	}
}

func (d *device) dispatchToTun(ctx context.Context) {
	defer d.wg.Done()
	for {
		clog.Trace(d.ctx, "ReadContext")
		pb := d.ReadContext(ctx)
		if pb == nil {
			break
		}
		if err := d.writePacket(pb); err != nil {
			clog.Errorf(ctx, "WritePacket failed: %v", err)
		}
		pb.DecRef()
	}
}
