package tcp

import (
	"bytes"
	"context"
	"time"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

func (h *handler) handleStreamControl(ctx context.Context, ctrl tunnel.Message) {
	switch ctrl.Code() {
	case tunnel.DialOK:
	case tunnel.DialReject, tunnel.Disconnect:
		dlog.Debugf(ctx, "   MGR %s, hard disconnect", h.id)
		h.Stop(ctx)
	case tunnel.KeepAlive:
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
		if h.packetLostTimer == nil {
			h.packetLostTimer = time.AfterFunc(5*time.Second, func() {
				h.Stop(ctx)
			})
		}
		return false
	}
}

func (h *handler) adjustReceiveWindow() {
	// Adjust window size based on current queue sizes. Both channels
	// are of ioChannelSize.
	inBuffer := float64(len(h.toMgrCh) + len(h.fromTun))
	bufSize := float64(2 * ioChannelSize)
	ratio := inBuffer / bufSize // 0.0 means empty, 1.0 is completely full
	ratio = 0.5 - ratio         // 0.5 means empty, below zero means more than half full

	windowSize := 0

	// windowSize will remain at zero as long as the buffer is more than half full
	if ratio > 0.0 {
		// Make window size dependent on the number o element on the queue
		ratio *= 2 // 1.0 means empty buffer
		windowSize = int(float64(maxReceiveWindow) * ratio)
	}

	// Strip the last 8 bits so that we don't change so often
	windowSize &^= 0xff
	h.setReceiveWindow(windowSize)
}

// readFromMgrLoop sends the packets read from the fromMgr channel to the TUN device
func (h *handler) readFromMgrLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			dlog.Errorf(ctx, "%+v", derror.PanicToError(r))
		}
	}()
	h.wg.Add(1)
	defer h.wg.Done()
	msgCh, fromMgrErrs := tunnel.ReadLoop(ctx, h.stream)
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-fromMgrErrs:
			if ok {
				dlog.Error(ctx, err)
			}
		case m, ok := <-msgCh:
			if !ok {
				return
			}
			if m.Code() != tunnel.Normal {
				h.handleStreamControl(ctx, m)
				continue
			}
			if h.state() != stateIdle {
				h.processPayload(ctx, m.Payload())
			}
		}
	}
}

// writeToMgrLoop sends the packets read from the toMgrCh channel to the traffic-manager device
func (h *handler) writeToMgrLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			dlog.Errorf(ctx, "%+v", derror.PanicToError(r))
		}
	}()
	h.wg.Add(1)
	defer h.wg.Done()

	// the time to wait until we flush in spite of not getting a PSH
	const flushDelay = 2 * time.Millisecond

	// Threshold when we flush in spite of not getting a PSH
	const maxBufSize = 0x10000

	var mgrWrite func(payload []byte) bool
	defer close(h.toMgrMsgCh)
	h.wg.Add(1)
	tunnel.WriteLoop(ctx, h.stream, h.toMgrMsgCh, &h.wg)
	mgrWrite = func(payload []byte) bool {
		select {
		case <-ctx.Done():
			return true
		case h.toMgrMsgCh <- tunnel.NewMessage(tunnel.Normal, payload):
			return false
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
		case pkt, ok := <-h.toMgrCh:
			if !ok {
				if buf.Len() > 0 {
					sendBuf()
				}
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
		}
	}
}

func (h *handler) sendStreamControl(ctx context.Context, code tunnel.MessageCode) {
	select {
	case <-ctx.Done():
	case h.toMgrMsgCh <- tunnel.NewMessage(code, nil):
	}
}
