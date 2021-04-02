package udp

import (
	"context"
	"sync"
	"time"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"

	"github.com/telepresenceio/telepresence/v2/pkg/connpool"

	"github.com/datawire/dlib/dlog"
)

type Handler struct {
	*Stream
	id        connpool.ConnID
	remove    func()
	fromTun   chan Datagram
	idleTimer *time.Timer
}

func (c *Handler) Close(_ context.Context) {
}

const ioChannelSize = 0x40
const idleDuration = time.Second

func (c *Handler) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	c.idleTimer = time.AfterFunc(idleDuration, func() {
		c.remove()
		cancel()
	})
	go c.writerLoop(ctx)
	<-ctx.Done()
}

func (c *Handler) NewDatagram(ctx context.Context, dg Datagram) {
	select {
	case <-ctx.Done():
	case c.fromTun <- dg:
	}
}

func NewHandler(stream *Stream, id connpool.ConnID, remove func()) *Handler {
	return &Handler{
		Stream:  stream,
		id:      id,
		remove:  remove,
		fromTun: make(chan Datagram, ioChannelSize),
	}
}

func (c *Handler) writerLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case dg := <-c.fromTun:
			c.idleTimer.Reset(idleDuration)

			udpHdr := dg.Header()
			dlog.Debugf(ctx, "-> SOC: %s", dg)
			err := c.bidiStream.Send(&manager.UDPDatagram{
				SourceIp:        c.id.Source(),
				SourcePort:      int32(c.id.SourcePort()),
				DestinationIp:   c.id.Destination(),
				DestinationPort: int32(c.id.DestinationPort()),
				Payload:         udpHdr.Payload(),
			})
			dg.SoftRelease()
			if err != nil {
				if ctx.Err() == nil {
					dlog.Error(ctx, err)
				}
				return
			}
		}
	}
}
