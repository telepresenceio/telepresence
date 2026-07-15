//go:build perf

package network

import (
	"context"
	"encoding/binary"
	"encoding/csv"
	"errors"
	"io/fs"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// udpServerAddr is the in-cluster raw-UDP throughput source (testdata/udpthroughput.yaml),
// reached by cluster DNS so the traffic rides the VPN -- the VIF UDP path whose endpoint
// buffers this experiment probes.
const udpServerAddr = "perf-udp:5202"

// The udpRequest wire format mirrors testdata/udpthroughput/main.go: a magic, the per-datagram
// payload size, a target rate (bytes/sec; 0 = unlimited), and a duration in ms. Data datagrams
// carry an 8-byte sequence number; the trailing done marker uses the max sequence value and
// carries the actual count the server sent.
const (
	udpReqMagic   = "UTP1"
	udpReqLen     = 20
	udpHdrLen     = 16
	udpDoneMarker = math.MaxUint64
)

// throughputResult is one bulk-UDP download's outcome: how many bytes/datagrams actually
// arrived through the VIF over the streaming window, and how many the server reported sending
// (from the trailing done marker; sent is 0 if that marker was itself lost).
type throughputResult struct {
	bytes   int64
	packets int64
	sent    uint64
	window  time.Duration
	err     error
}

// runUDPDownload asks the server to stream payloadSize-byte datagrams at targetBytesPerSec for
// dur, then counts what arrives through the VIF. A connected UDP socket (net.Dial) is used so
// the read side only accepts the server's replies, which arrive from the dialed service
// address via cluster conntrack -- exactly the flow the client opened. Sending the request is
// also what opens that flow through the VIF, so the reply stream rides back through the same
// tunneled UDP flow without an intercept.
func runUDPDownload(ctx context.Context, addr string, payloadSize int, targetBytesPerSec uint64, dur time.Duration) throughputResult {
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp", addr)
	if err != nil {
		return throughputResult{err: err}
	}
	defer conn.Close()

	req := make([]byte, udpReqLen)
	copy(req[0:4], udpReqMagic)
	binary.BigEndian.PutUint32(req[4:8], uint32(payloadSize))
	binary.BigEndian.PutUint64(req[8:16], targetBytesPerSec)
	binary.BigEndian.PutUint32(req[16:20], uint32(dur.Milliseconds()))
	if _, err := conn.Write(req); err != nil {
		return throughputResult{err: err}
	}

	res := throughputResult{window: dur}
	buf := make([]byte, 2048)
	// dur + grace: the server streams for dur, so give trailing/in-flight datagrams and the
	// done marker a little longer to arrive before the read deadline stops the loop.
	deadline := time.Now().Add(dur + 3*time.Second)
	for {
		_ = conn.SetReadDeadline(deadline)
		n, err := conn.Read(buf)
		if err != nil {
			break // deadline or closed: the server has stopped (or the done marker was lost)
		}
		if n < udpHdrLen {
			continue
		}
		if binary.BigEndian.Uint64(buf[0:8]) == udpDoneMarker {
			res.sent = binary.BigEndian.Uint64(buf[8:16])
			break
		}
		res.bytes += int64(n)
		res.packets++
		if ctx.Err() != nil {
			break
		}
	}
	return res
}

// summarizeThroughput reports goodput (Mbit/s delivered over the streaming window) and loss
// (percent of the datagrams the server said it sent that never arrived; 0 when the done marker
// was lost and sent is therefore unknown). Goodput is measured over the requested window
// rather than a wall-clock span, since the server streams for exactly that duration; a buffer
// that drops bursts shows up as fewer delivered bytes over the same window.
func summarizeThroughput(r throughputResult) (goodputMbit, lossPct float64) {
	if r.window > 0 {
		goodputMbit = float64(r.bytes*8) / r.window.Seconds() / 1e6
	}
	if r.sent > 0 && int64(r.sent) >= r.packets {
		lossPct = 100 * float64(int64(r.sent)-r.packets) / float64(r.sent)
	}
	return goodputMbit, lossPct
}

// warmupUDP drives the UDP path until it flows (a fresh session's DNS and tunnel may lag
// connect) and delivers datagrams, discarding the result, so session establishment does not
// land in the measured window.
func (c config) warmupUDP(t *testing.T, payloadSize int, targetBytesPerSec uint64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		r := runUDPDownload(ctx, udpServerAddr, payloadSize, targetBytesPerSec, 2*time.Second)
		cancel()
		if r.err == nil && r.packets > 0 {
			return
		}
		if time.Now().After(deadline) {
			if r.err != nil {
				t.Fatalf("warmup: udp path never became reachable: %v", r.err)
			}
			t.Fatal("warmup: udp path reachable but delivered no datagrams")
		}
		time.Sleep(2 * time.Second)
	}
}

// writeThroughputCSV appends one row per (transport) arm to vif-throughput.csv in outDir. It
// has its own header -- goodput/loss, not the latency percentiles writeCSV writes -- so the
// two experiment shapes do not share a file.
func writeThroughputCSV(t *testing.T, outDir string, rows [][]string) string {
	t.Helper()
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("create out dir: %v", err)
	}
	path := filepath.Join(outDir, "vif-throughput.csv")
	_, statErr := os.Stat(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open csv: %v", err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	if errors.Is(statErr, fs.ErrNotExist) {
		_ = w.Write([]string{"timestamp", "transport", "label", "rtt", "target_mbit_s", "goodput_mbit_s", "loss_pct", "received", "sent"})
	}
	for _, row := range rows {
		_ = w.Write(row)
	}
	w.Flush()
	return path
}

func throughputRow(ts string, tr transport, label, rtt string, targetMbit int, goodput, loss float64, r throughputResult) []string {
	return []string{
		ts, string(tr), label, rtt, strconv.Itoa(targetMbit),
		strconv.FormatFloat(goodput, 'f', 1, 64),
		strconv.FormatFloat(loss, 'f', 1, 64),
		strconv.FormatInt(r.packets, 10),
		strconv.FormatUint(r.sent, 10),
	}
}
