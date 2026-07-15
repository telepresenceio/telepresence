//go:build perf

package network

import (
	"context"
	"os"
	"testing"
	"time"
)

// VIF bulk-throughput knobs. This experiment probes the client-side VIF netstack's UDP
// endpoint buffers (gVisor's 32 KiB default, no receive auto-tuning; see
// perf/network/netstack-tuning.md) with a protocol-agnostic bulk UDP flow. It is about UDP in
// general, not QUIC: both tunnel transports terminate at the same client-side netstack, so the
// buffer is the same regardless of which carries the flow.
var (
	vifPayloadSize = 1200 // bytes per datagram, ~one wire-sized packet
	// vifTargetMbit is the offered rate. It must be high enough that the in-flight data at the
	// injected RTT (target/8 * RTT bytes) exceeds the 32 KiB UDP buffer, so a too-small buffer
	// shows up as loss / reduced goodput rather than being absorbed.
	vifTargetMbit = 200
	vifWindowDur  = 15 * time.Second
)

// TestVIFBulkThroughput drives a single bulk UDP download through the VIF at a fixed offered
// rate and measures delivered goodput and loss, once per available tunnel transport.
//
// A netem DELAY (PERF_IMPAIR_NODE, loss 0) is what raises the bandwidth-delay product past the
// VIF's 32 KiB UDP endpoint buffer; without an injected RTT the BDP stays under the buffer and
// the buffer is never exercised. This is deliberately NOT a loss experiment: the question is
// whether the buffer window-limits a bursty bulk flow, not how the flow recovers from loss.
//
// The measurement is a before/after: run it with today's stock buffers, then again after a
// candidate buffer change in pkg/vif/stack.go (rebuild the client; PERF_LABEL tags the rows,
// e.g. "stock" vs "tuned"), and compare perf/results/vif-throughput.csv. Per
// netstack-tuning.md, only commit a buffer size that moves the number.
//
// The grpc arm always runs (no forwarder needed); the quic arm runs only when a forwarder
// endpoint is configured (PERF_QUIC_EXTERNAL_HOST). If the VIF buffer is the constraint both
// arms should move together -- the "helps both transports equally" claim the note makes.
func TestVIFBulkThroughput(t *testing.T) {
	cfg := loadConfig(t)
	arms := []transport{transportGRPC}
	if cfg.quicExternalHost != "" {
		arms = append(arms, transportQUIC)
	}
	if !cfg.impairing() {
		t.Log("no PERF_IMPAIR_NODE/PERF_NETEM_IFACE set: without an injected RTT the BDP stays " +
			"under the 32 KiB buffer and the buffer is not exercised; recording a clean-network baseline only")
	}
	label := os.Getenv("PERF_LABEL")
	rtt := "none"
	if cfg.impairNode != "" {
		rtt = cfg.netemDelay
	}
	targetBps := uint64(vifTargetMbit) * 1_000_000 / 8

	ts := time.Now().UTC().Format(time.RFC3339)
	var rows [][]string
	for _, tr := range arms {
		func() {
			cfg.quit(t)
			cfg.helmUninstall(t)
			cfg.helmInstall(t, tr)
			t.Cleanup(func() { cfg.quit(t); cfg.helmUninstall(t) })

			cfg.kubectl(t, "-n", cfg.appNamespace, "apply", "-f", "testdata/udpthroughput.yaml")
			// 240s: on Autopilot the first schedule may provision a node.
			cfg.kubectl(t, "-n", cfg.appNamespace, "rollout", "status", "deploy/perf-udp", "--timeout=240s")
			t.Cleanup(func() {
				_ = cfg.kubectlQuiet("-n", cfg.appNamespace, "delete", "-f", "testdata/udpthroughput.yaml")
			})

			cfg.connect(t)
			cfg.assertTransport(t, tr)
			cfg.warmupUDP(t, vifPayloadSize, targetBps)

			removeLoss := cfg.applyLoss(t, 0) // impairNode mode: delay only, no loss
			ctx, cancel := context.WithTimeout(context.Background(), vifWindowDur+time.Minute)
			res := runUDPDownload(ctx, udpServerAddr, vifPayloadSize, targetBps, vifWindowDur)
			cancel()
			removeLoss()
			if res.err != nil {
				t.Fatalf("%s: udp download failed: %v", tr, res.err)
			}

			goodput, loss := summarizeThroughput(res)
			t.Logf("%s @ rtt=%s: target=%d Mbit/s goodput=%.1f Mbit/s loss=%.1f%% (%d received, %d sent)",
				tr, rtt, vifTargetMbit, goodput, loss, res.packets, res.sent)
			rows = append(rows, throughputRow(ts, tr, label, rtt, vifTargetMbit, goodput, loss, res))
		}()
	}

	path := writeThroughputCSV(t, cfg.outDir, rows)
	t.Logf("results written to %s", path)
}
