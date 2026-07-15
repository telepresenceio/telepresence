// Command udpthroughput is a minimal in-cluster raw-UDP throughput source used by the
// VIF bulk-throughput experiment (perf/network/vifthroughput_test.go). It exists to probe
// the client-side VIF netstack's UDP endpoint buffers with a protocol-agnostic bulk UDP
// flow -- NOT an inner reliable protocol like HTTP/3, which self-paces and reacts to loss
// and so would mask the buffer's raw drop behavior (see perf/network/netstack-tuning.md).
//
// Protocol (deliberately tiny, stdlib only):
//
//   - The client sends one REQUEST datagram naming a per-datagram payload size, a target
//     rate (bytes/sec; 0 = as fast as possible), and a duration. Sending it is also what
//     opens the flow through the telepresence VIF (the client's source port), so the
//     server's reply stream rides back through the same tunneled UDP flow without an
//     intercept.
//   - The server then streams DATA datagrams of the requested size back to that address for
//     the duration, each carrying an 8-byte sequence number and the planned total count, so
//     the client can compute goodput (bytes/window) and loss (received/total) even if the
//     trailing marker is itself lost.
//   - At the end the server sends a DONE marker (seq = math.MaxUint64) a few times, carrying
//     the actual number of data datagrams it sent.
//
// The download direction (server -> client) is deliberate: it exercises the VIF UDP
// endpoint's SEND buffer -- the 32 KiB gVisor default that holds cluster->client datagrams
// queued for delivery out the TUN -- which is the buffer a bulk UDP *consumer* (media, a
// download) hits.
package main

import (
	"encoding/binary"
	"log"
	"math"
	"net"
	"os"
	"time"
)

const (
	reqMagic   = "UTP1"
	reqLen     = 20 // magic(4) + payloadSize(4) + rate(8) + durationMs(4)
	hdrLen     = 16 // seq(8) + total(8)
	doneMarker = math.MaxUint64
	doneRepeat = 5
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "5202"
	}
	addr, err := net.ResolveUDPAddr("udp", ":"+port)
	if err != nil {
		log.Fatalf("resolve :%s: %v", port, err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Fatalf("listen :%s: %v", port, err)
	}
	// A generous socket buffer on the SERVER side so the server is never the bottleneck;
	// the experiment is about the CLIENT's VIF buffer, not this one.
	_ = conn.SetWriteBuffer(16 << 20)
	_ = conn.SetReadBuffer(16 << 20)
	log.Printf("udpthroughput listening on :%s", port)

	buf := make([]byte, 2048)
	for {
		n, client, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("read: %v", err)
			continue
		}
		if n < reqLen || string(buf[:4]) != reqMagic {
			continue // not a request; ignore
		}
		payloadSize := int(binary.BigEndian.Uint32(buf[4:8]))
		rate := binary.BigEndian.Uint64(buf[8:16])
		durationMs := binary.BigEndian.Uint32(buf[16:20])
		if payloadSize < hdrLen {
			payloadSize = hdrLen
		}
		if payloadSize > 1500 {
			payloadSize = 1500
		}
		go blast(conn, client, payloadSize, rate, time.Duration(durationMs)*time.Millisecond)
	}
}

// blast streams data datagrams to client for the given duration, paced to rate bytes/sec
// (rate 0 = unlimited), then sends the DONE marker. Each data datagram carries its sequence
// number and the planned total so the client can measure loss without the marker.
func blast(conn *net.UDPConn, client *net.UDPAddr, payloadSize int, rate uint64, dur time.Duration) {
	var plannedTotal uint64
	if rate > 0 {
		plannedTotal = uint64(float64(rate) * dur.Seconds() / float64(payloadSize))
	}
	pkt := make([]byte, payloadSize)
	binary.BigEndian.PutUint64(pkt[8:16], plannedTotal)

	start := time.Now()
	deadline := start.Add(dur)
	var seq uint64
	for time.Now().Before(deadline) {
		binary.BigEndian.PutUint64(pkt[0:8], seq)
		if _, err := conn.WriteToUDP(pkt, client); err != nil {
			log.Printf("write to %s: %v", client, err)
			return
		}
		seq++
		if rate > 0 {
			// Pace: the seq-th datagram should leave at start + seq*payloadSize/rate.
			target := start.Add(time.Duration(float64(seq) * float64(payloadSize) / float64(rate) * float64(time.Second)))
			if d := time.Until(target); d > 0 {
				time.Sleep(d)
			}
		}
	}

	done := make([]byte, payloadSize)
	binary.BigEndian.PutUint64(done[0:8], doneMarker)
	binary.BigEndian.PutUint64(done[8:16], seq) // actual count sent
	for range doneRepeat {
		_, _ = conn.WriteToUDP(done, client)
		time.Sleep(5 * time.Millisecond)
	}
	log.Printf("blast to %s done: sent %d datagrams (%d bytes each) in %v", client, seq, payloadSize, time.Since(start))
}
