package tcp

import (
	"bytes"
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/connpool"
)

func (h *handler) HandleControl(ctx context.Context, ctrl *connpool.ControlMessage) {
	switch ctrl.Code {
	case connpool.ConnectOK:
		synPacket := h.synPacket
		h.synPacket = nil
		if synPacket != nil {
			defer synPacket.Release()
			h.sendSyn(ctx, synPacket)
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
	case connpool.ReadClosed, connpool.WriteClosed:
		h.Close(ctx)
	}
}

func (h *handler) HandleMessage(ctx context.Context, cm *manager.ConnMessage) {
	select {
	case <-ctx.Done():
	case h.fromMgr <- cm:
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

// the time to wait until we flush in spite of not getting a PSH
const flushDelay = 10 * time.Millisecond

// writeToMgrLoop sends the packages read from the toMgr channel to the traffic-manager device
func (h *handler) writeToMgrLoop(ctx context.Context) {
	mgrWrite := func(payload []byte) bool {
		dlog.Debugf(ctx, "-> MGR %s, len %d", h.id, len(payload))
		if err := h.SendMsg(&manager.ConnMessage{ConnId: []byte(h.id), Payload: payload}); err != nil {
			if ctx.Err() == nil && atomic.LoadInt32(h.dispatcherClosing) == 0 && h.state() < stateFinWait2 {
				dlog.Errorf(ctx, "   CON %s failed to write to dispatcher's remote endpoint: %v", h.id, err)
			}
			return true
		}
		return false
	}

	flushTimer := time.NewTimer(flushDelay)
	buf := bytes.Buffer{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-flushTimer.C:
			if buf.Len() > 0 {
				if mgrWrite(buf.Bytes()) {
					return
				}
				buf.Reset()
			}
		case pkt := <-h.toMgr:
			drained := flushTimer.Stop()
			h.adjustReceiveWindow()
			tcpHdr := pkt.Header()
			payload := tcpHdr.Payload()
			if tcpHdr.PSH() {
				if buf.Len() == 0 {
					if mgrWrite(payload) { // save extra copying by bypassing buf.
						return
					}
				} else {
					buf.Write(payload)
					if mgrWrite(buf.Bytes()) {
						return
					}
					buf.Reset()
				}
			} else {
				buf.Write(payload)
				if drained {
					flushTimer.Reset(flushDelay)
				}
			}
			pkt.Release()
		}
	}
}

func (h *handler) sendConnControl(ctx context.Context, code connpool.ControlCode) error {
	pkt := connpool.ConnControl(h.id, code)
	dlog.Debugf(ctx, "-> MGR %s, code %s", h.id, code)
	if err := h.SendMsg(pkt); err != nil {
		return fmt.Errorf("failed to send control package: %v", err)
	}
	return nil
}
