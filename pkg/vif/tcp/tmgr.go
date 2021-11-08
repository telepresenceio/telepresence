package tcp

import (
	"bytes"
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/connpool"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// handleControl for versions < 2.4.5
// Deprecated
func (h *handler) handleControl(ctx context.Context, ctrl connpool.Control) {
	switch ctrl.Code() {
	case connpool.Connect:
		// Peer wants to initialize a new connection
		h.setState(ctx, stateSynSent)
		h.setSequence(uint32(h.RandomSequence()))
		h.setReceiveWindow(maxReceiveWindow)
		h.sendSyn(ctx)
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

func (h *handler) handleStreamControl(ctx context.Context, ctrl tunnel.Message) {
	switch ctrl.Code() {
	case tunnel.DialOK:
	case tunnel.DialReject, tunnel.Disconnect:
		h.Close(ctx)
	case tunnel.KeepAlive:
	}
}

// HandleMessage for versions < 2.4.5
// Deprecated
func (h *handler) HandleMessage(ctx context.Context, msg connpool.Message) {
	select {
	case <-ctx.Done():
	case h.fromMgr <- msg:
	}
}

func (h *handler) sendToMgr(ctx context.Context, pkt Packet) bool {
	select {
	case h.toMgrCh <- pkt:
		h.adjustReceiveWindow()
		if h.packetLostTimer != nil {
			h.packetLostTimer.Stop()
			h.packetLostTimer = nil
		}
		return true
	default:
		// Manager doesn't keep up. Packet loss!
		dlog.Debugf(ctx, "-> MGR %s packet lost!", pkt)
		pkt.Release()
		if h.packetLostTimer == nil {
			h.packetLostTimer = time.AfterFunc(5*time.Second, func() {
				h.Close(ctx)
			})
		}
		return false
	}
}

func (h *handler) adjustReceiveWindow() {
	// Adjust window size based on current queue sizes.
	queueFactor := ioChannelSize - (len(h.toMgrCh) + len(h.fromTun))
	windowSize := 0
	if queueFactor > 0 {
		// Make window size dependent on the number o element on the queue
		windowSize = queueFactor * (maxReceiveWindow / ioChannelSize)

		// Strip the last 8 bits so that we don't change so often
		windowSize &^= 0xff
	}
	h.setReceiveWindow(windowSize)
}

// readFromMgrLoop sends the packets read from the fromMgr channel to the TUN device
func (h *handler) readFromMgrLoop(ctx context.Context) {
	h.wg.Add(1)
	defer func() {
		h.Close(ctx)
		h.wg.Done()
	}()
	fromMgrCh, fromMgrErrs := tunnel.ReadLoop(ctx, h.stream)
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.tunDone:
			return
		case err := <-fromMgrErrs:
			dlog.Error(ctx, err)
		case m := <-fromMgrCh:
			if m == nil {
				return
			}

			select {
			case <-ctx.Done():
				return
			case <-h.tunDone:
				return
			default:
			}

			if m.Code() != tunnel.Normal {
				h.handleStreamControl(ctx, m)
				continue
			}
			h.processPayload(ctx, m.Payload())
		}
	}
}

// readFromMgrMux sends the packets read from the fromMgr channel to the TUN device
// Deprecated
func (h *handler) readFromMgrMux(ctx context.Context) {
	defer close(h.readyToFin)

	// We use a timer that we reset on each iteration instead of a ticker to prevent drift between
	// the select loop and the ticker's interval. Otherwise, suppose we spend 499ms processing
	// a message from fromMgr, a ticker would only give us a 1ms wait before checking h.isClosing
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	for {
		timer.Stop()
		select {
		case <-timer.C:
		default:
		}

		timer.Reset(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if atomic.LoadInt32(&h.isClosing) > 0 {
				return
			}
			continue
		case cm := <-h.fromMgr:
			if cm == nil {
				return
			}
			if ctrl, ok := cm.(connpool.Control); ok {
				h.handleControl(ctx, ctrl)
				continue
			}
			h.processPayload(ctx, cm.Payload())
		}
	}
}

// writeToMgrLoop sends the packets read from the toMgrCh channel to the traffic-manager device
func (h *handler) writeToMgrLoop(ctx context.Context) {
	// the time to wait until we flush in spite of not getting a PSH
	const flushDelay = 2 * time.Millisecond

	// Threshold when we flush in spite of not getting a PSH
	const maxBufSize = 0x10000

	var mgrWrite func(payload []byte) bool
	if h.muxTunnel != nil {
		mgrWrite = func(payload []byte) bool {
			dlog.Tracef(ctx, "-> MGR %s, len %d", h.id, len(payload))
			if err := h.muxTunnel.Send(ctx, connpool.NewMessage(h.id, payload)); err != nil {
				if ctx.Err() == nil && atomic.LoadInt32(h.dispatcherClosing) == 0 && h.state() < stateFinWait2 {
					dlog.Errorf(ctx, "   CON %s failed to write to dispatcher's remote endpoint: %v", h.id, err)
				}
				return true
			}
			return false
		}
	} else {
		defer close(h.toMgrMsgCh)
		tunnel.WriteLoop(ctx, h.stream, h.toMgrMsgCh)
		mgrWrite = func(payload []byte) bool {
			select {
			case <-ctx.Done():
				return true
			case h.toMgrMsgCh <- tunnel.NewMessage(tunnel.Normal, payload):
				return false
			}
		}
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
		case <-h.tunDone:
			return
		case pkt := <-h.toMgrCh:
			if pkt == nil {
				return
			}
			h.adjustReceiveWindow()
			tcpHdr := pkt.Header()
			payload := tcpHdr.Payload()
			if tcpHdr.PSH() || buf.Len()+len(payload) >= maxBufSize {
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

// sendConnControl for versions < 2.4.5
// Deprecated
func (h *handler) sendConnControl(ctx context.Context, code connpool.ControlCode) error {
	dlog.Debugf(ctx, "-> MGR %s, code %s", h.id, code)
	if err := h.muxTunnel.Send(ctx, connpool.NewControl(h.id, code, nil)); err != nil {
		return fmt.Errorf("failed to send control packet: %w", err)
	}
	return nil
}

func (h *handler) sendStreamControl(ctx context.Context, code tunnel.MessageCode) {
	select {
	case <-ctx.Done():
	case h.toMgrMsgCh <- tunnel.NewMessage(code, nil):
	}
}
