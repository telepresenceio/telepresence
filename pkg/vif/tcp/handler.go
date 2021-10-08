package tcp

import (
	"context"
	"encoding/binary"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/connpool"
	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/buffer"
	"github.com/telepresenceio/telepresence/v2/pkg/vif/ip"
)

type state int32

const (
	// simplified server-side tcp states
	stateSynReceived = state(iota)
	stateSynSent
	stateEstablished
	stateFinWait1
	stateFinWait2
	stateTimedWait
	stateIdle
)

func (s state) String() (txt string) {
	switch s {
	case stateIdle:
		txt = "IDLE"
	case stateSynSent:
		txt = "SYN SENT"
	case stateSynReceived:
		txt = "SYN RECEIVED"
	case stateEstablished:
		txt = "ESTABLISHED"
	case stateFinWait1:
		txt = "FIN_WAIT_1"
	case stateFinWait2:
		txt = "FIN_WAIT_2"
	case stateTimedWait:
		txt = "TIMED WAIT"
	default:
		panic("unknown state")
	}
	return txt
}

const myWindowScale = 8
const maxReceiveWindow = 4096 << myWindowScale // 1MB

var maxSegmentSize = buffer.DataPool.MTU - (20 + HeaderLen) // Ethernet MTU of 1500 - 20 byte IP header and 20 byte TCP header
var ioChannelSize = maxReceiveWindow / maxSegmentSize

type queueElement struct {
	sequence uint32
	retries  int32
	cTime    time.Time
	packet   Packet
	next     *queueElement
}

type quitReason int

const (
	pleaseContinue = quitReason(iota)
	quitByContext
	quitByReset
	quitByUs
	quitByPeer
	quitByBoth
)

type PacketHandler interface {
	connpool.Handler

	// HandlePacket handles a packet that was read from the TUN device
	HandlePacket(ctx context.Context, pkt Packet)
}

type StreamCreator func(ctx context.Context) (tunnel.Stream, error)

type handler struct {
	streamCreator StreamCreator

	// Handle will have either a connection specific stream or a muxTunnel (the old style)
	// depending on what the handler is talking to
	stream tunnel.Stream

	// id identifies this connection. It contains source and destination IPs and ports
	id tunnel.ConnID

	// remove is the function that removes this instance from the pool
	remove func()

	// TUN I/O
	toTun   ip.Writer
	fromTun chan Packet

	// the dispatcher signals its intent to close in dispatcherClosing. 0 == running, 1 == closing, 2 == closed
	dispatcherClosing *int32

	// Channel to use when sending packets to the traffic-manager
	toMgrCh chan Packet

	// Channel to use when sending messages to the traffic-manager
	toMgrMsgCh chan tunnel.Message

	// queue where unacked elements are placed until they are acked
	ackWaitQueue     *queueElement
	ackWaitQueueSize uint32

	// oooQueue is where out-of-order packets are placed until they can be processed
	oooQueue *queueElement

	// wfState is the current workflow state
	wfState state

	// seq is the sequence that we provide in the packets we send to TUN
	seq uint32

	// seqAcked is the last sequence acked by the peer
	seqAcked uint32

	// finalSeq is the ack sent with FIN when a connection is closing.
	finalSeq uint32

	// myWindow and is the actual size of my window
	myWindow int64

	// peerSeqAcked was the last ack sent to the peer
	peerSeqAcked uint32

	// peerWindow is the actual size of the peers window
	peerWindow int64

	// peerWindowScale is the number of bits to shift the windowSize of received packet to
	// determine the actual peerWindow
	peerWindowScale uint8

	// peerMaxSegmentSize is the maximum size of a segment sent to the peer (not counting IP-header)
	peerMaxSegmentSize uint16

	// sendLock and sendCondition are used when throttling writes to the TUN device
	sendLock      sync.Mutex
	sendCondition *sync.Cond

	// random generator for initial sequence number
	rnd *rand.Rand

	// synPacket is the initial syn packet received on a connect request. It is
	// dropped once the manager responds to the connect attempt
	// Deprecated
	synPacket Packet

	muxTunnel connpool.MuxTunnel    // Deprecated
	fromMgr   chan connpool.Message // Deprecated

	// isClosing indicates whether Close() has been called on the TUN-device
	// Deprecated
	isClosing int32

	// readyToFin will be closed once it is safe for the handler to send a FIN packet and terminate the connection
	// Deprecated
	readyToFin chan interface{}
}

func NewHandler(
	streamCreator StreamCreator,
	muxTunnel connpool.MuxTunnel,
	dispatcherClosing *int32,
	toTun ip.Writer,
	id tunnel.ConnID,
	remove func(),
	rndSource rand.Source,
) PacketHandler {
	h := &handler{
		streamCreator:     streamCreator,
		muxTunnel:         muxTunnel,
		id:                id,
		remove:            remove,
		toTun:             toTun,
		dispatcherClosing: dispatcherClosing,
		fromTun:           make(chan Packet, ioChannelSize),
		toMgrCh:           make(chan Packet, ioChannelSize),
		toMgrMsgCh:        make(chan tunnel.Message),
		myWindow:          maxReceiveWindow,
		wfState:           stateIdle,
		rnd:               rand.New(rndSource),
		fromMgr:           make(chan connpool.Message, ioChannelSize),
		readyToFin:        make(chan interface{}),
	}
	h.sendCondition = sync.NewCond(&h.sendLock)
	return h
}

func (h *handler) RandomSequence() int32 {
	return h.rnd.Int31()
}

func (h *handler) HandlePacket(ctx context.Context, pkt Packet) {
	select {
	case <-ctx.Done():
	case h.fromTun <- pkt:
	}
}

func (h *handler) Close(ctx context.Context) {
	if h.state() == stateEstablished || h.state() == stateSynReceived {
		atomic.StoreInt32(&h.isClosing, 1)
		// Wait for the fromMgr queue to drain before sending a FIN
		<-h.readyToFin

		h.setState(ctx, stateFinWait1)
		h.sendFin(ctx, true)
	}
}

func (h *handler) Start(ctx context.Context) error {
	go h.processResends(ctx)
	go func() {
		defer func() {
			if h.muxTunnel != nil {
				_ = h.sendConnControl(ctx, connpool.Disconnect)
			} else if h.stream != nil {
				_ = h.stream.CloseSend(ctx)
			}
			h.remove()
		}()
		h.processPackets(ctx)
	}()
	if h.muxTunnel != nil {
		h.fromMgr = make(chan connpool.Message, ioChannelSize)
		h.readyToFin = make(chan interface{})
		go h.readFromMgrMux(ctx) // Needs to start here to handle initial control packets
	}
	return nil
}

func (h *handler) sendToTun(ctx context.Context, pkt Packet) {
	if err := h.toTun.Write(ctx, pkt); err != nil {
		dlog.Errorf(ctx, "!! TUN %s: %v", h.id, err)
	}
}

func (h *handler) newResponse(ipPayloadLen int, withAck bool) Packet {
	pkt := NewPacket(ipPayloadLen, h.id.Destination(), h.id.Source(), withAck)
	ipHdr := pkt.IPHeader()
	ipHdr.SetL4Protocol(ipproto.TCP)
	ipHdr.SetChecksum()

	tcpHdr := Header(ipHdr.Payload())
	tcpHdr.SetDataOffset(5)
	tcpHdr.SetSourcePort(h.id.DestinationPort())
	tcpHdr.SetDestinationPort(h.id.SourcePort())
	h.myWindowToHeader(tcpHdr)
	return pkt
}

func (h *handler) sendAck(ctx context.Context) {
	pkt := h.newResponse(HeaderLen, false)
	tcpHdr := pkt.Header()
	tcpHdr.SetACK(true)
	tcpHdr.SetSequence(h.sequence())
	tcpHdr.SetAckNumber(h.peerSequenceAcked())
	tcpHdr.SetChecksum(pkt.IPHeader())
	h.sendToTun(ctx, pkt)
}

func (h *handler) sendFin(ctx context.Context, expectAck bool) {
	pkt := h.newResponse(HeaderLen, true)
	tcpHdr := pkt.Header()
	tcpHdr.SetFIN(true)
	tcpHdr.SetACK(true)
	tcpHdr.SetSequence(h.sequence())
	tcpHdr.SetAckNumber(h.peerSequenceAcked())
	tcpHdr.SetChecksum(pkt.IPHeader())
	if expectAck {
		h.pushToAckWait(ctx, 1, pkt)
		h.finalSeq = h.sequence()
	}
	h.sendToTun(ctx, pkt)
}

func (h *handler) sendSynReply(ctx context.Context, syn Packet) {
	synHdr := syn.Header()
	if !synHdr.SYN() {
		return
	}
	h.sendSyn(ctx, synHdr.Sequence()+1, true)
}

func (h *handler) sendSyn(ctx context.Context, ackNumber uint32, ack bool) {
	hl := HeaderLen
	hl += 8 // for the Maximum Segment Size option and for the Window Scale option

	pkt := h.newResponse(hl, true)
	tcpHdr := pkt.Header()
	tcpHdr.SetSYN(true)
	tcpHdr.SetACK(ack)
	tcpHdr.SetSequence(h.sequence())
	tcpHdr.SetAckNumber(ackNumber)

	// adjust data offset to account for options
	tcpHdr.SetDataOffset(hl / 4)

	opts := tcpHdr.OptionBytes()
	opts[0] = byte(maximumSegmentSize)
	opts[1] = 4
	binary.BigEndian.PutUint16(opts[2:], uint16(maxSegmentSize))

	opts[4] = byte(windowScale)
	opts[5] = 3
	opts[6] = myWindowScale
	opts[7] = byte(noOp)

	tcpHdr.SetChecksum(pkt.IPHeader())

	h.setPeerSequenceAcked(tcpHdr.AckNumber())
	h.sendToTun(ctx, pkt)
	h.pushToAckWait(ctx, 1, pkt)
}

func (h *handler) processPayload(ctx context.Context, data []byte) {
	start := 0
	n := len(data)
	for n > start {
		h.sendLock.Lock()
		window := int(h.peerWindow) - int(h.sequence()-h.seqAcked)
		for window <= 0 {
			if window <= 0 {
				// The intended receiver is currently not accepting data. We must
				// wait for the window to increase.
				dlog.Debugf(ctx, "   CON %s TCP window is zero", h.id)
			}
			h.sendCondition.Wait()
			if h.state() != stateEstablished {
				h.sendLock.Unlock()
				return
			}
			window = int(h.peerWindow) - int(h.sequence()-h.seqAcked)
		}
		h.sendLock.Unlock()

		mxSend := n - start
		if mxSend > int(h.peerMaxSegmentSize) {
			mxSend = int(h.peerMaxSegmentSize)
		}
		if mxSend > window {
			mxSend = window
		}

		pkt := h.newResponse(HeaderLen+mxSend, true)
		ipHdr := pkt.IPHeader()
		tcpHdr := pkt.Header()
		ipHdr.SetPayloadLen(HeaderLen + mxSend)
		ipHdr.SetChecksum()

		tcpHdr.SetACK(true)
		tcpHdr.SetSequence(h.sequence())
		tcpHdr.SetAckNumber(h.peerSequenceAcked())

		end := start + mxSend
		copy(tcpHdr.Payload(), data[start:end])
		tcpHdr.SetPSH(end == n)
		tcpHdr.SetChecksum(ipHdr)

		h.sendToTun(ctx, pkt)
		h.pushToAckWait(ctx, uint32(mxSend), pkt)

		// Decrease the window size with the bytes that we just sent unless it's already updated
		// from a received packet
		atomic.CompareAndSwapInt64(&h.peerWindow, int64(window), int64(window-mxSend))
		start = end
	}
}

func (h *handler) idle(ctx context.Context, syn Packet) quitReason {
	tcpHdr := syn.Header()
	if tcpHdr.RST() {
		dlog.Errorf(ctx, "   CON %s, got RST while idle", h.id)
		return quitByUs
	}
	if !tcpHdr.SYN() {
		h.sendToTun(ctx, syn.Reset())
		return quitByUs
	}

	synOpts, err := options(tcpHdr)
	if err != nil {
		dlog.Error(ctx, err)
		h.sendToTun(ctx, syn.Reset())
		return quitByUs
	}
	for _, synOpt := range synOpts {
		switch synOpt.kind() {
		case maximumSegmentSize:
			h.peerMaxSegmentSize = binary.BigEndian.Uint16(synOpt.data())
			dlog.Tracef(ctx, "   CON %s maximum segment size %d", h.id, h.peerMaxSegmentSize)
		case windowScale:
			h.peerWindowScale = synOpt.data()[0]
			dlog.Tracef(ctx, "   CON %s window scale %d", h.id, h.peerWindowScale)
		case selectiveAckPermitted:
			dlog.Tracef(ctx, "   CON %s selective acknowledgments permitted", h.id)
		default:
			dlog.Tracef(ctx, "   CON %s option %d with len %d", h.id, synOpt.kind(), synOpt.len())
		}
	}

	h.setSequence(uint32(h.RandomSequence()))
	h.setState(ctx, stateSynReceived)
	if h.muxTunnel != nil {
		h.synPacket = syn
		err = h.sendConnControl(ctx, connpool.Connect)
	} else {
		// Reply to the SYN, then establish a connection. We send a reset if that fails.
		h.sendSynReply(ctx, syn)
		defer syn.Release()
		if h.stream, err = h.streamCreator(ctx); err == nil {
			go h.readFromMgrLoop(ctx)
		}
	}
	if err != nil {
		h.synPacket = nil
		dlog.Error(ctx, err)
		if err := h.toTun.Write(ctx, syn.Reset()); err != nil {
			dlog.Errorf(ctx, "!! CON %s, send of RST failed: %v", h.id, err)
		}
		return quitByUs
	}
	return pleaseContinue
}

func (h *handler) synReceived(ctx context.Context, pkt Packet) quitReason {
	release := true
	defer func() {
		if release {
			pkt.Release()
		}
	}()

	tcpHdr := pkt.Header()
	if tcpHdr.RST() {
		return quitByReset
	}
	if !tcpHdr.ACK() {
		return pleaseContinue
	}

	h.ackReceived(ctx, tcpHdr.AckNumber())
	h.setState(ctx, stateEstablished)
	go h.writeToMgrLoop(ctx)

	pl := len(tcpHdr.Payload())
	h.setPeerSequenceAcked(tcpHdr.Sequence() + uint32(pl))
	if pl != 0 {
		h.setSequence(h.sequence() + uint32(pl))
		h.pushToAckWait(ctx, uint32(pl), pkt)
		if !h.sendToMgr(ctx, pkt) {
			return quitByContext
		}
		release = false
	}
	return pleaseContinue
}

func (h *handler) handleReceived(ctx context.Context, pkt Packet) quitReason {
	state := h.state()
	release := true
	defer func() {
		if release {
			pkt.Release()
		}
	}()

	tcpHdr := pkt.Header()
	if tcpHdr.RST() {
		h.setState(ctx, stateIdle)
		return quitByReset
	}

	if !tcpHdr.ACK() {
		// Just ignore packets that have no ack
		return pleaseContinue
	}

	ackNbr := tcpHdr.AckNumber()
	h.ackReceived(ctx, ackNbr)
	if state == stateTimedWait {
		h.setState(ctx, stateIdle)
		return quitByPeer
	}

	sq := tcpHdr.Sequence()
	lastAck := h.peerSequenceAcked()
	payloadLen := len(tcpHdr.Payload())
	switch {
	case sq == lastAck:
		if state == stateFinWait1 && ackNbr == h.finalSeq && !tcpHdr.FIN() {
			h.setState(ctx, stateFinWait2)
			return pleaseContinue
		}
	case sq > lastAck:
		// Oops. Packet loss! Let sender know by sending an ACK so that we ack the receipt
		// and also tell the sender about our expected number
		h.sendAck(ctx)
		h.addOutOfOrderPacket(ctx, pkt)
		release = false
		return pleaseContinue
	case sq == lastAck-1 && payloadLen == 0:
		// keep alive
		h.sendAck(ctx)
		if h.muxTunnel != nil {
			_ = h.sendConnControl(ctx, connpool.KeepAlive)
		} else {
			h.sendStreamControl(ctx, tunnel.KeepAlive)
		}
		return pleaseContinue
	default:
		// resend of already acknowledged packet. Just ignore
		dlog.Debug(ctx, "client resends already acked packet")
		return pleaseContinue
	}
	if tcpHdr.RST() {
		return quitByReset
	}

	switch {
	case payloadLen > 0:
		h.setPeerSequenceAcked(lastAck + uint32(payloadLen))
		if !h.sendToMgr(ctx, pkt) {
			return quitByContext
		}
		release = false
	case tcpHdr.FIN():
		h.setPeerSequenceAcked(lastAck + 1)
	default:
		// don't ack an ack
		return pleaseContinue
	}
	h.sendAck(ctx)

	switch state {
	case stateEstablished:
		if tcpHdr.FIN() {
			h.sendFin(ctx, false)
			h.setState(ctx, stateTimedWait)
			return quitByPeer
		}
	case stateFinWait1:
		if tcpHdr.FIN() {
			h.setState(ctx, stateTimedWait)
			return quitByBoth
		}
		h.setState(ctx, stateFinWait2)
	case stateFinWait2:
		if tcpHdr.FIN() {
			return quitByUs
		}
	}
	return pleaseContinue
}

func (h *handler) processPackets(ctx context.Context) {
	for {
		select {
		case pkt := <-h.fromTun:
			dlog.Tracef(ctx, "<- TUN %s", pkt)
			if !h.processPacket(ctx, pkt) {
				return
			}
			for {
				continueProcessing, next := h.processNextOutOfOrderPacket(ctx)
				if !continueProcessing {
					return
				}
				if !next {
					break
				}
			}
		case <-ctx.Done():
			h.setState(ctx, stateIdle)
			return
		}
	}
}

func (h *handler) processPacket(ctx context.Context, pkt Packet) bool {
	h.peerWindowFromHeader(ctx, pkt.Header())
	var end quitReason
	switch h.state() {
	case stateIdle:
		end = h.idle(ctx, pkt)
	case stateSynReceived:
		end = h.synReceived(ctx, pkt)
	default:
		end = h.handleReceived(ctx, pkt)
	}
	switch end {
	case quitByReset, quitByContext:
		h.setState(ctx, stateIdle)
		return false
	case quitByUs, quitByPeer, quitByBoth:
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		h.processPackets(ctx)
		return false
	default:
		return true
	}
}

const initialResendDelay = 2
const maxResends = 7

type resend struct {
	packet Packet
	secs   int
	next   *resend
}

func (h *handler) processResends(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		now := time.Now()
		var resends *resend
		h.sendLock.Lock()
		var prev *queueElement
		for el := h.ackWaitQueue; el != nil; {
			secs := initialResendDelay << el.retries // 2, 4, 8, 16, ...
			deadLine := el.cTime.Add(time.Duration(secs) * time.Second)
			if deadLine.Before(now) {
				el.retries++
				if el.retries > maxResends {
					dlog.Errorf(ctx, "   CON %s, packet resent %d times, giving up", h.id, maxResends)
					// Drop from queue and point to next
					el = el.next
					if prev == nil {
						h.ackWaitQueue = el
					} else {
						prev.next = el
					}
					continue
				}

				// reverse (i.e. put in right order since ackWaitQueue is in fact reversed)
				resends = &resend{packet: el.packet, secs: secs, next: resends}
			}
			prev = el
			el = el.next
		}
		h.sendLock.Unlock()
		for resends != nil {
			dlog.Debugf(ctx, "   CON %s, Resending %s after having waited for %d seconds", h.id, resends.packet, resends.secs)
			h.sendToTun(ctx, resends.packet)
			resends = resends.next
		}
	}
}

func (h *handler) pushToAckWait(ctx context.Context, seqAdd uint32, pkt Packet) {
	h.sendLock.Lock()
	h.ackWaitQueue = &queueElement{
		sequence: h.addSequence(seqAdd),
		cTime:    time.Now(),
		packet:   pkt,
		next:     h.ackWaitQueue,
	}
	h.ackWaitQueueSize++
	dlog.Tracef(ctx, "   CON %s, Ack-queue size %d", h.id, h.ackWaitQueueSize)
	h.sendLock.Unlock()
}

func (h *handler) ackReceived(ctx context.Context, seq uint32) {
	h.sendLock.Lock()
	// ack-queue is guaranteed to be sorted descending on sequence so we cut from the packet with
	// a sequence less than or equal to the received sequence.
	sq := h.sequence()
	oldWindow := int(h.peerWindow) - int(sq-h.seqAcked)
	h.seqAcked = seq
	newWindow := int(h.peerWindow) - int(sq-h.seqAcked)

	el := h.ackWaitQueue
	var prev *queueElement
	for el != nil && el.sequence > seq {
		prev = el
		el = el.next
	}

	if el != nil {
		if prev == nil {
			h.ackWaitQueue = nil
		} else {
			prev.next = nil
		}
		for {
			el.packet.Release()
			h.ackWaitQueueSize--
			if el = el.next; el == nil {
				break
			}
		}
		dlog.Tracef(ctx, "   CON %s, Ack-queue size %d", h.id, h.ackWaitQueueSize)
	}
	h.sendLock.Unlock()
	if oldWindow <= 0 && newWindow > 0 {
		dlog.Debugf(ctx, "   CON %s, TCP window %d after ack", h.id, newWindow)
		h.sendCondition.Signal()
	}
}

func (h *handler) processNextOutOfOrderPacket(ctx context.Context) (bool, bool) {
	seq := h.peerSequenceAcked()
	var prev *queueElement
	for el := h.oooQueue; el != nil; el = el.next {
		if el.sequence == seq {
			if prev != nil {
				prev.next = el.next
			} else {
				h.oooQueue = el.next
			}
			dlog.Debugf(ctx, "   CON %s, Processing out-of-order packet %s", h.id, el.packet)
			return h.processPacket(ctx, el.packet), true
		}
		prev = el
	}
	return true, false
}

func (h *handler) addOutOfOrderPacket(ctx context.Context, pkt Packet) {
	dlog.Debugf(ctx, "   CON %s, Keeping out-of-order packet %s", h.id, pkt)
	h.oooQueue = &queueElement{
		sequence: pkt.Header().Sequence(),
		cTime:    time.Now(),
		packet:   pkt,
		next:     h.oooQueue,
	}
}

func (h *handler) state() state {
	return state(atomic.LoadInt32((*int32)(&h.wfState)))
}

func (h *handler) setState(ctx context.Context, s state) {
	oldState := h.state()
	if oldState != s {
		dlog.Debugf(ctx, "   CON %s, state %s -> %s", h.id, h.state(), s)
		atomic.StoreInt32((*int32)(&h.wfState), int32(s))
		if oldState == stateEstablished {
			// Unblock any sender when moving from stateEstablished
			h.sendCondition.Signal()
		}
	}
}

// sequence is the sequence number of the packets that this client
// sends to the TUN device.
func (h *handler) sequence() uint32 {
	return atomic.LoadUint32(&h.seq)
}

func (h *handler) addSequence(v uint32) uint32 {
	return atomic.AddUint32(&h.seq, v)
}

func (h *handler) setSequence(v uint32) {
	atomic.StoreUint32(&h.seq, v)
}

// peerSequenceAcked was the last ack sent to the peer
func (h *handler) peerSequenceAcked() uint32 {
	return atomic.LoadUint32(&h.peerSeqAcked)
}

func (h *handler) setPeerSequenceAcked(v uint32) {
	atomic.StoreUint32(&h.peerSeqAcked, v)
}

func (h *handler) peerWindowFromHeader(ctx context.Context, tcpHeader Header) {
	h.sendLock.Lock()
	sq := h.sequence()
	oldWindow := int(h.peerWindow) - int(sq-h.seqAcked)
	h.peerWindow = int64(tcpHeader.WindowSize()) << h.peerWindowScale
	newWindow := int(h.peerWindow) - int(sq-h.seqAcked)
	h.sendLock.Unlock()
	if oldWindow <= 0 && newWindow > 0 {
		dlog.Debugf(ctx, "   CON %s, TCP window %d after window update", h.id, newWindow)
		h.sendCondition.Signal()
	}
}

func (h *handler) myWindowToHeader(tcpHeader Header) {
	tcpHeader.SetWindowSize(uint16(h.receiveWindow() >> myWindowScale))
}

func (h *handler) receiveWindow() int {
	return int(atomic.LoadInt64(&h.myWindow))
}

func (h *handler) setReceiveWindow(v int) {
	atomic.StoreInt64(&h.myWindow, int64(v))
}
