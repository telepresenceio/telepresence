package tcp

import (
	"bytes"
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/connpool"
)

func (h *handler) handleControl(ctx context.Context, ctrl connpool.Control) {
	switch ctrl.Code() {
	case connpool.Connect:
		// Peer wants to initialize a new connection
		h.setState(ctx, stateSynSent)
		h.setSequence(uint32(h.RandomSequence()))
		h.setReceiveWindow(maxReceiveWindow)
		h.windowScale = 6
		h.sendSyn(ctx, 0, false)
	case connpool.ConnectOK:
		synPacket := h.synPacket
		h.synPacket = nil
		if synPacket != nil {
			defer synPacket.Release()
			h.sendSynReply(ctx, synPacket)
		}
	case connpool.ConnectReject:
		synPacket := h.synPacket
		h.synPacket = nil
		if synPacket != nil {
			// We won't ack the SYN, so release and
			// remove it from the ack queue
			synPacket.Release()
			h.sendLock.Lock()
			h.ackWaitQueue = nil
			h.sendLock.Unlock()
		}
		h.setState(ctx, stateFinWait1)
		h.sendFin(ctx, true)
	case connpool.ReadClosed, connpool.WriteClosed, connpool.Disconnect:
		_ = h.sendConnControl(ctx, connpool.DisconnectOK)
		h.Close(ctx)
	case connpool.DisconnectOK:
		h.Close(ctx)
	case connpool.KeepAlive:
	}
}

func (h *handler) HandleMessage(ctx context.Context, msg connpool.Message) {
	select {
	case <-ctx.Done():
	case h.fromMgr <- msg:
	}
}

func (h *handler) sendToMgr(ctx context.Context, pkt Packet) bool {
	select {
	case h.toMgr <- pkt:
		h.adjustReceiveWindow()
		return true
	case <-ctx.Done():
		return false
	}
}

// writeToMgrLoop sends the packages read from the toMgr channel to the traffic-manager device
func (h *handler) writeToMgrLoop(ctx context.Context) {
	// the time to wait until we flush in spite of not getting a PSH
	const flushDelay = 2 * time.Millisecond

	// Threshold when we flush in spite of not getting a PSH
	const maxBufSize = 0x10000

	mgrWrite := func(payload []byte) bool {
		dlog.Debugf(ctx, "-> MGR %s, len %d", h.id, len(payload))
		if err := h.Send(ctx, connpool.NewMessage(h.id, payload)); err != nil {
			if ctx.Err() == nil && atomic.LoadInt32(h.dispatcherClosing) == 0 && h.state() < stateFinWait2 {
				dlog.Errorf(ctx, "   CON %s failed to write to dispatcher's remote endpoint: %v", h.id, err)
			}
			return true
		}
		return false
	}

	flushTimer := time.NewTimer(flushDelay)
	flushTimer.Stop() // Not used until we write to buf

	buf := bytes.Buffer{}

	sendBuf := func() {
		if mgrWrite(buf.Bytes()) {
			return
		}
		buf.Reset()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-flushTimer.C:
			if buf.Len() > 0 {
				sendBuf()
			}
		case pkt := <-h.toMgr:
			h.adjustReceiveWindow()
			tcpHdr := pkt.Header()
			payload := tcpHdr.Payload()
			if tcpHdr.PSH() || buf.Len()+len(payload) > maxBufSize {
				if buf.Len() == 0 {
					if mgrWrite(payload) { // save extra copying by bypassing buf.
						return
					}
				} else {
					flushTimer.Stop() // It doesn't matter if the flushTime.C isn't empty. It will fire on a zero buffer
					buf.Write(payload)
					sendBuf()
				}
			} else {
				if buf.Len() == 0 {
					flushTimer.Reset(flushDelay)
				}
				buf.Write(payload)
			}
			pkt.Release()
		}
	}
}

func (h *handler) sendConnControl(ctx context.Context, code connpool.ControlCode) error {
	pkt := connpool.NewControl(h.id, code, nil)
	dlog.Debugf(ctx, "-> MGR %s, code %s", h.id, code)
	if err := h.Send(ctx, pkt); err != nil {
		return fmt.Errorf("failed to send control package: %w", err)
	}
	return nil
}
