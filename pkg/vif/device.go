package vif

import (
	"context"
	"io"
	"net"
	"sync"

	"gvisor.dev/gvisor/pkg/bufferv2"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/stack"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/routing"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/buffer"
)

type device struct {
	sync.Mutex
	*channel.Endpoint
	ctx   context.Context
	wg    sync.WaitGroup
	dev   *nativeDevice
	table routing.Table
}

type Device interface {
	stack.LinkEndpoint
	io.Closer
	Index() int32
	Name() string
	AddSubnet(context.Context, *net.IPNet) error
	RemoveSubnet(context.Context, *net.IPNet) error
	SetDNS(context.Context, string, net.IP, []string) (err error)
}

const defaultDevMtu = 1500

// Queue length for outbound packet, arriving at fd side for read. Overflow
// causes packet drops. gVisor implementation-specific.
const defaultDevOutQueueLen = 1024

var _ Device = (*device)(nil)

// OpenTun creates a new TUN device and ensures that it is up and running.
func OpenTun(ctx context.Context, routingTable routing.Table) (Device, error) {
	dev, err := openTun(ctx)
	if err != nil {
		return nil, err
	}

	return &device{
		Endpoint: channel.New(defaultDevOutQueueLen, defaultDevMtu, ""),
		ctx:      ctx,
		dev:      dev,
		table:    routingTable,
	}, nil
}

func (d *device) Attach(dp stack.NetworkDispatcher) {
	d.Lock()
	d.Endpoint.Attach(dp)
	d.Unlock()
	if dp == nil {
		// Stack is closing
		return
	}
	dlog.Info(d.ctx, "Starting Endpoint")
	ctx, cancel := context.WithCancel(d.ctx)
	d.wg.Add(2)
	go d.tunToDispatch(cancel)
	go d.dispatchToTun(ctx)
}

func (d *device) subnetToRoute(subnet *net.IPNet) (*routing.Route, error) {
	gw := make(net.IP, len(subnet.IP))
	copy(gw, subnet.IP)
	gw[len(gw)-1] += 1
	iface, err := net.InterfaceByName(d.Name())
	if err != nil {
		return nil, err
	}
	return &routing.Route{
		LocalIP:   subnet.IP,
		RoutedNet: subnet,
		Interface: iface,
		Gateway:   gw,
	}, nil
}

// AddSubnet adds a subnet to this TUN device and creates a route for that subnet which
// is associated with the device (removing the device will automatically remove the route).
func (d *device) AddSubnet(ctx context.Context, subnet *net.IPNet) (err error) {
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "AddSubnet", trace.WithAttributes(attribute.Stringer("tel2.subnet", subnet)))
	defer tracing.EndAndRecord(span, err)
	if err := d.dev.addSubnet(ctx, subnet); err != nil {
		return err
	}
	route, err := d.subnetToRoute(subnet)
	if err != nil {
		return err
	}
	return d.table.Add(ctx, route)
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
	route, err := d.subnetToRoute(subnet)
	if err != nil {
		return err
	}
	if err := d.table.Remove(ctx, route); err != nil {
		return err
	}
	// Staticcheck screams if this is ctx, span := because it thinks the context argument is being overwritten before being used.
	sCtx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "RemoveSubnet", trace.WithAttributes(attribute.Stringer("tel2.subnet", subnet)))
	defer tracing.EndAndRecord(span, err)
	return d.dev.removeSubnet(sCtx, subnet)
}

func (d *device) Wait() {
	d.wg.Wait()
	dlog.Info(d.ctx, "Endpoint done")
}

func (d *device) tunToDispatch(cancel context.CancelFunc) {
	defer func() {
		cancel()
		d.wg.Done()
	}()
	buf := buffer.NewData(0x10000)
	data := buf.Buf()
	for ok := true; ok; {
		n, err := d.dev.readPacket(buf)
		if err != nil {
			d.Lock()
			ok = d.IsAttached()
			d.Unlock()
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
			Payload: bufferv2.MakeWithData(data[:n]),
		})

		d.Lock()
		if ok = d.IsAttached(); ok {
			d.InjectInbound(ipv, pb)
		}
		d.Unlock()
		pb.DecRef()
	}
}

func (d *device) dispatchToTun(ctx context.Context) {
	defer d.wg.Done()
	buf := buffer.NewData(0x10000)
	for {
		pb := d.ReadContext(ctx)
		if pb.IsNil() {
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
