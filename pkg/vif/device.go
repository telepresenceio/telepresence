package vif

import (
	"context"
	"io"
	"net"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
	vifBuffer "github.com/telepresenceio/telepresence/v2/pkg/vif/buffer"
)

type device struct {
	*channel.Endpoint
	ctx context.Context
	wg  sync.WaitGroup
	dev *nativeDevice
}

type Device interface {
	stack.LinkEndpoint
	io.Closer
	Index() int32
	Name() string
	AddSubnet(context.Context, *net.IPNet) error
	RemoveSubnet(context.Context, *net.IPNet) error
	SetDNS(context.Context, string, net.IP, []string) (err error)
	WaitForDevice()
}

const defaultDevMtu = 1500

// Queue length for outbound packet, arriving at fd side for read. Overflow
// causes packet drops. gVisor implementation-specific.
const defaultDevOutQueueLen = 1024

var _ Device = (*device)(nil)

// OpenTun creates a new TUN device and ensures that it is up and running.
func OpenTun(ctx context.Context) (Device, error) {
	dev, err := openTun(ctx)
	if err != nil {
		return nil, err
	}

	return &device{
		Endpoint: channel.New(defaultDevOutQueueLen, defaultDevMtu, ""),
		ctx:      ctx,
		dev:      dev,
	}, nil
}

func (d *device) Attach(dp stack.NetworkDispatcher) {
	go func() {
		d.Endpoint.Attach(dp)
		if dp == nil {
			// Stack is closing
			return
		}
		dlog.Info(d.ctx, "Starting Endpoint")
		ctx, cancel := context.WithCancel(d.ctx)
		d.wg.Add(2)
		go d.tunToDispatch(cancel)
		d.dispatchToTun(ctx)
	}()
}

// AddSubnet adds a subnet to this TUN device and creates a route for that subnet which
// is associated with the device (removing the device will automatically remove the route).
func (d *device) AddSubnet(ctx context.Context, subnet *net.IPNet) (err error) {
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "AddSubnet", trace.WithAttributes(attribute.Stringer("tel2.subnet", subnet)))
	defer tracing.EndAndRecord(span, err)
	return d.dev.addSubnet(ctx, subnet)
}

func (d *device) Close() error {
	return d.dev.Close()
}

// Index returns the index of this device.
func (d *device) Index() int32 {
	return d.dev.index()
}

// Name returns the name of this device, e.g. "tun0".
func (d *device) Name() string {
	return d.dev.name
}

// SetDNS sets the DNS configuration for the device on the windows platform.
func (d *device) SetDNS(ctx context.Context, clusterDomain string, server net.IP, domains []string) (err error) {
	return d.dev.setDNS(ctx, clusterDomain, server, domains)
}

func (d *device) SetMTU(mtu int) error {
	return d.dev.setMTU(mtu)
}

// RemoveSubnet removes a subnet from this TUN device and also removes the route for that subnet which
// is associated with the device.
func (d *device) RemoveSubnet(ctx context.Context, subnet *net.IPNet) (err error) {
	// Staticcheck screams if this is ctx, span := because it thinks the context argument is being overwritten before being used.
	sCtx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "RemoveSubnet", trace.WithAttributes(attribute.Stringer("tel2.subnet", subnet)))
	defer tracing.EndAndRecord(span, err)
	return d.dev.removeSubnet(sCtx, subnet)
}

func (d *device) WaitForDevice() {
	d.wg.Wait()
	dlog.Info(d.ctx, "Endpoint done")
}

func (d *device) tunToDispatch(cancel context.CancelFunc) {
	defer func() {
		cancel()
		d.wg.Done()
	}()
	buf := vifBuffer.NewData(0x10000)
	data := buf.Buf()
	for ok := true; ok; {
		n, err := d.dev.readPacket(buf)
		if err != nil {
			ok = d.IsAttached()
			if ok && d.ctx.Err() == nil {
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
			Payload: buffer.MakeWithData(data[:n]),
		})

		d.InjectInbound(ipv, pb)
		pb.DecRef()
	}
}

func (d *device) dispatchToTun(ctx context.Context) {
	defer d.wg.Done()
	buf := vifBuffer.NewData(0x10000)
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
		if _, err := d.dev.writePacket(buf, 0); err != nil {
			dlog.Errorf(ctx, "WritePacket failed: %v", err)
		}
	}
}
