package tcp

import (
	"context"
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/connpool"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/buffer"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/ip"
	"github.com/telepresenceio/telepresence/v2/pkg/tun/socks"
)

type state int32

const (
	// simplified server-side tcp states
	stateSynReceived = state(iota)
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

const maxReceiveWindow = 65535
const maxSendWindow = 65535
const ioChannelSize = 0x40

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

type Workflow struct {
	// id identifies this connection. It contains source and destination IPs and ports
	id connpool.ConnID

	// remove is the function that removes this instance from the pool
	remove func()

	// TUN channels
	toTun   chan<- ip.Packet
	fromTun chan Packet

	// the dispatcher signals its intent to close in dispatcherClosing. 0 == running, 1 == closing, 2 == closed
	dispatcherClosing *int32

	// Dialer to user when establishing a socks connection
	socksDialer socks.Dialer
	socksConn   net.Conn

	// Channel to use when sending packages to the socksConn
	toSocks chan Packet

	// queue where unacked elements are placed until they are acked
	ackWaitQueue     *queueElement
	ackWaitQueueLock sync.Mutex

	// oooQueue is where out-of-order packages are placed until they can be processed
	oooQueue *queueElement

	// wfState is the current workflow state
	wfState state

	// seq is the sequence that we provide in the packages we send to TUN
	seq uint32

	// lastAck is the last ackNumber that we received from TUN
	lastAck uint32

	// finalSeq is the ack sent with FIN when a connection is closing.
	finalSeq uint32

	// sendWnd and rcvWnd controls the size of the send and receive window
	sendWnd int32
	rcvWnd  int32
}

func NewWorkflow(socksDialer socks.Dialer, cloeState *int32, toTunCh chan<- ip.Packet, id connpool.ConnID, remove func()) *Workflow {
	return &Workflow{
		id:                id,
		remove:            remove,
		socksDialer:       socksDialer,
		dispatcherClosing: cloeState,
		toTun:             toTunCh,
		fromTun:           make(chan Packet, ioChannelSize),
		toSocks:           make(chan Packet, ioChannelSize),
		sendWnd:           int32(maxSendWindow),
		rcvWnd:            int32(maxReceiveWindow),
		wfState:           stateIdle,
	}
}

func (c *Workflow) NewPacket(ctx context.Context, pkt Packet) {
	select {
	case <-ctx.Done():
	case c.fromTun <- pkt:
	}
}

func (c *Workflow) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer func() {
		if c.socksConn != nil {
			c.socksConn.Close()
		}
		c.remove()
		wg.Done()
	}()
	go c.processResends(ctx)
	c.processPackets(ctx)
}

func (c *Workflow) Close(ctx context.Context) {
	if c.state() == stateEstablished {
		c.setState(ctx, stateFinWait1)
		c.sendFin(ctx, true)
	}
}

func (c *Workflow) adjustReceiveWindow() {
	queueSize := len(c.toSocks)
	windowSize := maxReceiveWindow
	if queueSize > ioChannelSize/4 {
		windowSize -= queueSize * (maxReceiveWindow / ioChannelSize)
	}
	c.setReceiveWindow(uint16(windowSize))
}

func (c *Workflow) sendToSocks(ctx context.Context, pkt Packet) bool {
	select {
	case c.toSocks <- pkt:
		c.adjustReceiveWindow()
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *Workflow) sendToTun(ctx context.Context, pkt Packet) {
	select {
	case <-ctx.Done():
	case c.toTun <- pkt:
	}
}

func (c *Workflow) newResponse(ipPlayloadLen int, withAck bool) Packet {
	pkt := NewPacket(ipPlayloadLen, c.id.Destination(), c.id.Source(), withAck)
	ipHdr := pkt.IPHeader()
	ipHdr.SetL4Protocol(unix.IPPROTO_TCP)
	ipHdr.SetChecksum()

	tcpHdr := Header(ipHdr.Payload())
	tcpHdr.SetDataOffset(5)
	tcpHdr.SetSourcePort(c.id.DestinationPort())
	tcpHdr.SetDestinationPort(c.id.SourcePort())
	tcpHdr.SetWindowSize(c.receiveWindow())
	return pkt
}

func (c *Workflow) sendAck(ctx context.Context) {
	pkt := c.newResponse(HeaderLen, false)
	tcpHdr := pkt.Header()
	tcpHdr.SetACK(true)
	tcpHdr.SetSequence(c.sequence())
	tcpHdr.SetAckNumber(c.sequenceLastAcked())
	tcpHdr.SetChecksum(pkt.IPHeader())
	c.sendToTun(ctx, pkt)
}

func (c *Workflow) sendFin(ctx context.Context, expectAck bool) {
	pkt := c.newResponse(HeaderLen, true)
	tcpHdr := pkt.Header()
	tcpHdr.SetFIN(true)
	tcpHdr.SetACK(true)
	tcpHdr.SetSequence(c.sequence())
	tcpHdr.SetAckNumber(c.sequenceLastAcked())
	tcpHdr.SetChecksum(pkt.IPHeader())
	if expectAck {
		c.pushToAckWait(1, pkt)
		c.finalSeq = c.sequence()
	}
	c.sendToTun(ctx, pkt)
}

func (c *Workflow) sendSyn(ctx context.Context, syn Packet) {
	synHdr := syn.Header()
	if !synHdr.SYN() {
		return
	}
	hl := HeaderLen
	if synHdr.ECE() {
		hl += 4
	}
	pkt := c.newResponse(hl, true)
	tcpHdr := pkt.Header()
	tcpHdr.SetSYN(true)
	tcpHdr.SetACK(true)
	tcpHdr.SetSequence(c.sequence())
	tcpHdr.SetAckNumber(synHdr.Sequence() + 1)
	if synHdr.ECE() {
		tcpHdr.SetDataOffset(6)
		opts := tcpHdr.OptionBytes()
		opts[0] = 2
		opts[1] = 4
		binary.BigEndian.PutUint16(opts[2:], uint16(buffer.DataPool.MTU-HeaderLen))
	}
	tcpHdr.SetChecksum(pkt.IPHeader())

	c.setSequenceLastAcked(tcpHdr.AckNumber())
	c.sendToTun(ctx, pkt)
	c.pushToAckWait(1, pkt)
}

func (c *Workflow) socksWriterLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case pkt := <-c.toSocks:
			c.adjustReceiveWindow()
			tcpHdr := pkt.Header()
			data := tcpHdr.Payload()
			for len(data) > 0 {
				n, err := c.socksConn.Write(data)
				if err != nil {
					if ctx.Err() == nil && atomic.LoadInt32(c.dispatcherClosing) == 0 && c.state() < stateFinWait2 {
						dlog.Errorf(ctx, "failed to write to dispatcher's remote endpoint: %v", err)
					}
					return
				}
				data = data[n:]
			}
			pkt.Release()
		}
	}
}

func (c *Workflow) socksReaderLoop(ctx context.Context) {
	var bothHeadersLen int
	if c.id.IsIPv4() {
		bothHeadersLen = ipv4.HeaderLen + HeaderLen
	} else {
		bothHeadersLen = ipv6.HeaderLen + HeaderLen
	}

	for {
		window := c.sendWindow()
		if window == 0 {
			// The intended receiver is currently not accepting data. We must
			// wait for the window to increase.
			dlog.Debugf(ctx, "%s TCP window is zero", c.id)
			for window == 0 {
				dtime.SleepWithContext(ctx, 10*time.Microsecond)
				window = c.sendWindow()
			}
		}

		maxRead := int(window)
		if maxRead > buffer.DataPool.MTU-bothHeadersLen {
			maxRead = buffer.DataPool.MTU - bothHeadersLen
		}

		pkt := c.newResponse(HeaderLen+maxRead, true)
		ipHdr := pkt.IPHeader()
		tcpHdr := pkt.Header()
		n, err := c.socksConn.Read(tcpHdr.Payload())
		if err != nil {
			pkt.Release()
			if ctx.Err() == nil && atomic.LoadInt32(c.dispatcherClosing) == 0 && c.state() < stateFinWait2 {
				dlog.Errorf(ctx, "failed to read from dispatcher's remote endpoint: %v", err)
				if c.state() == stateEstablished {
					c.sendFin(ctx, true)
				}
			}
			return
		}
		if n == 0 {
			pkt.Release()
			continue
		}
		ipHdr.SetPayloadLen(n + HeaderLen)
		ipHdr.SetChecksum()

		tcpHdr.SetACK(true)
		tcpHdr.SetPSH(true)
		tcpHdr.SetSequence(c.sequence())
		tcpHdr.SetAckNumber(c.sequenceLastAcked())
		tcpHdr.SetChecksum(ipHdr)

		c.sendToTun(ctx, pkt)
		c.pushToAckWait(uint32(n), pkt)

		// Decrease the window size with the bytes that we just sent unless it's already updated
		// from a received package
		window -= window - uint16(n)
		atomic.CompareAndSwapInt32(&c.sendWnd, int32(window), int32(window))
	}
}

func (c *Workflow) idle(ctx context.Context, syn Packet) quitReason {
	defer syn.Release()
	conn, err := c.socksDialer.DialContext(ctx, c.id.Source(), c.id.SourcePort(), c.id.Destination(), c.id.DestinationPort())
	if err != nil {
		dlog.Errorf(ctx, "Unable to connect to socks server: %v", err)
		select {
		case <-ctx.Done():
			return quitByContext
		case c.toTun <- syn.Reset():
		}
		return quitByReset
	}
	c.socksConn = conn

	c.setSequence(1)
	c.sendSyn(ctx, syn)
	c.setState(ctx, stateSynReceived)
	return pleaseContinue
}

func (c *Workflow) synReceived(ctx context.Context, pkt Packet) quitReason {
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

	c.ackReceived(tcpHdr.AckNumber())
	c.setState(ctx, stateEstablished)
	go c.socksWriterLoop(ctx)
	go c.socksReaderLoop(ctx)

	pl := len(tcpHdr.Payload())
	c.setSequenceLastAcked(tcpHdr.Sequence() + uint32(pl))
	if pl != 0 {
		if !c.sendToSocks(ctx, pkt) {
			return quitByContext
		}
		release = false
	}
	return pleaseContinue
}

func (c *Workflow) handleReceived(ctx context.Context, pkt Packet) quitReason {
	state := c.state()
	release := true
	defer func() {
		if release {
			pkt.Release()
		}
	}()

	tcpHdr := pkt.Header()
	if tcpHdr.RST() {
		c.setState(ctx, stateIdle)
		return quitByReset
	}

	if !tcpHdr.ACK() {
		// Just ignore packages that has no ack
		return pleaseContinue
	}

	ackNbr := tcpHdr.AckNumber()
	c.ackReceived(ackNbr)
	if state == stateTimedWait {
		c.setState(ctx, stateIdle)
		return quitByPeer
	}

	sq := tcpHdr.Sequence()
	lastAck := c.sequenceLastAcked()
	switch {
	case sq == lastAck:
		if state == stateFinWait1 && ackNbr == c.finalSeq && !tcpHdr.FIN() {
			c.setState(ctx, stateFinWait2)
			return pleaseContinue
		}
	case sq > lastAck:
		// Oops. Package loss! Let sender know by sending an ACK so that we ack the receipt
		// and also tell the sender about our expected number
		c.sendAck(ctx)
		c.addOutOfOrderPackage(ctx, pkt)
		release = false
		return pleaseContinue
	default:
		// resend of already acknowledged package. Just ignore
		return pleaseContinue
	}
	if tcpHdr.RST() {
		return quitByReset
	}

	switch {
	case len(tcpHdr.Payload()) > 0:
		c.setSequenceLastAcked(lastAck + uint32(len(tcpHdr.Payload())))
		if !c.sendToSocks(ctx, pkt) {
			return quitByContext
		}
		release = false
	case tcpHdr.FIN():
		c.setSequenceLastAcked(lastAck + 1)
	default:
		// don't ack acks
		return pleaseContinue
	}
	c.sendAck(ctx)

	switch state {
	case stateEstablished:
		if tcpHdr.FIN() {
			c.sendFin(ctx, false)
			c.setState(ctx, stateTimedWait)
			return quitByPeer
		}
	case stateFinWait1:
		if tcpHdr.FIN() {
			c.setState(ctx, stateTimedWait)
			return quitByBoth
		}
		c.setState(ctx, stateFinWait2)
	case stateFinWait2:
		if tcpHdr.FIN() {
			return quitByUs
		}
	}
	return pleaseContinue
}

func (c *Workflow) processPackets(ctx context.Context) {
	for {
		select {
		case pkt := <-c.fromTun:
			if !c.processPacket(ctx, pkt) {
				return
			}
			for {
				continueProcessing, next := c.processNextOutOfOrderPackage(ctx)
				if !continueProcessing {
					return
				}
				if !next {
					break
				}
			}
		case <-ctx.Done():
			c.setState(ctx, stateIdle)
			return
		}
	}
}

func (c *Workflow) processPacket(ctx context.Context, pkt Packet) bool {
	c.setSendWindow(pkt.Header().WindowSize())
	var end quitReason
	switch c.state() {
	case stateIdle:
		end = c.idle(ctx, pkt)
	case stateSynReceived:
		end = c.synReceived(ctx, pkt)
	default:
		end = c.handleReceived(ctx, pkt)
	}
	switch end {
	case quitByReset:
		return false
	case quitByContext:
		c.setState(ctx, stateIdle)
		return false
	case quitByUs, quitByPeer, quitByBoth:
		func() {
			ctx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			c.processPackets(ctx)
		}()
		return false
	default:
		return true
	}
}

const initialResendDelay = 2
const maxResends = 7

func (c *Workflow) processResends(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	for {
		select {
		case <-ctx.Done():
			ticker.Stop()
			return
		case <-ticker.C:
			now := time.Now()
			c.ackWaitQueueLock.Lock()
			var prev *queueElement
			for el := c.ackWaitQueue; el != nil; {
				secs := initialResendDelay << el.retries // 2, 4, 8, 16, ...
				deadLine := el.cTime.Add(time.Duration(secs) * time.Second)
				if deadLine.Before(now) {
					el.retries++
					if el.retries > maxResends {
						dlog.Errorf(ctx, "package resent %d times, giving up", maxResends)
						// Drop from queue and point to next
						el = el.next
						if prev == nil {
							c.ackWaitQueue = el
						} else {
							prev.next = el
						}
						continue
					}
					dlog.Debugf(ctx, "Resending %s after having waited for %d seconds", el.packet, secs)
					c.sendToTun(ctx, el.packet)
				}
				prev = el
				el = el.next
			}
			c.ackWaitQueueLock.Unlock()
		}
	}
}

func (c *Workflow) pushToAckWait(seqAdd uint32, pkt Packet) {
	c.ackWaitQueueLock.Lock()
	c.ackWaitQueue = &queueElement{
		sequence: c.addSequence(seqAdd),
		cTime:    time.Now(),
		packet:   pkt,
		next:     c.ackWaitQueue,
	}
	c.ackWaitQueueLock.Unlock()
}

func (c *Workflow) ackReceived(seq uint32) {
	c.ackWaitQueueLock.Lock()
	var prev *queueElement
	for el := c.ackWaitQueue; el != nil && el.sequence <= seq; el = el.next {
		if prev != nil {
			prev.next = el.next
		} else {
			c.ackWaitQueue = el.next
		}
		prev = el
		el.packet.Release()
	}
	c.ackWaitQueueLock.Unlock()
}

func (c *Workflow) processNextOutOfOrderPackage(ctx context.Context) (bool, bool) {
	seq := c.sequenceLastAcked()
	var prev *queueElement
	for el := c.oooQueue; el != nil; el = el.next {
		if el.sequence == seq {
			if prev != nil {
				prev.next = el.next
			} else {
				c.oooQueue = el.next
			}
			dlog.Debugf(ctx, "Processing out-of-order package %s", el.packet)
			return c.processPacket(ctx, el.packet), true
		}
		prev = el
	}
	return true, false
}

func (c *Workflow) addOutOfOrderPackage(ctx context.Context, pkt Packet) {
	dlog.Debugf(ctx, "Keeping out-of-order package %s", pkt)
	c.oooQueue = &queueElement{
		sequence: pkt.Header().Sequence(),
		cTime:    time.Now(),
		packet:   pkt,
		next:     c.oooQueue,
	}
}

func (c *Workflow) state() state {
	return state(atomic.LoadInt32((*int32)(&c.wfState)))
}

func (c *Workflow) setState(_ context.Context, s state) {
	// dlog.Debugf(ctx, "state %s -> %s", c.state(), s)
	atomic.StoreInt32((*int32)(&c.wfState), int32(s))
}

// sequence is the sequence number of the packages that this client
// sends to the TUN device.
func (c *Workflow) sequence() uint32 {
	return atomic.LoadUint32(&c.seq)
}

func (c *Workflow) addSequence(v uint32) uint32 {
	return atomic.AddUint32(&c.seq, v)
}

func (c *Workflow) setSequence(v uint32) {
	atomic.StoreUint32(&c.seq, v)
}

// sequenceLastAcked is the last received sequence that this client has ACKed
func (c *Workflow) sequenceLastAcked() uint32 {
	return atomic.LoadUint32(&c.lastAck)
}

func (c *Workflow) setSequenceLastAcked(v uint32) {
	atomic.StoreUint32(&c.lastAck, v)
}

func (c *Workflow) sendWindow() uint16 {
	return uint16(atomic.LoadInt32(&c.sendWnd))
}

func (c *Workflow) setSendWindow(v uint16) {
	atomic.StoreInt32(&c.sendWnd, int32(v))
}

func (c *Workflow) receiveWindow() uint16 {
	return uint16(atomic.LoadInt32(&c.rcvWnd))
}

func (c *Workflow) setReceiveWindow(v uint16) {
	atomic.StoreInt32(&c.rcvWnd, int32(v))
}
