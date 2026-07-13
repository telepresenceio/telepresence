//go:build perf

package perf

import (
	"context"
	"testing"
	"time"
)

// Experiment 1 knobs. Kept as vars (not env) because they define the shape of
// the published result; override in a fork if you are exploring, not per run.
var (
	exp1Streams      = 50              // concurrent downloads sharing the tunnel
	exp1PayloadBytes = 8 * 1024 * 1024 // per-download size; must match testdata/payload.yaml's initContainer
	exp1LossPercents = []float64{0, 1, 3}
	// exp1MinP99Ratio is the pass threshold at the highest loss level: the
	// gRPC transport's p99 must be at least this many times the QUIC transport's
	// p99, i.e. QUIC's independent streams must demonstrably contain the tail
	// that head-of-line blocking on the shared gRPC connection inflates. A
	// relative threshold (not an absolute latency) is what makes the assertion
	// portable across clusters and networks.
	exp1MinP99Ratio = 2.0
)

// TestExperiment1_HeadOfLineBlockingUnderLoss drives exp1Streams concurrent
// downloads through the tunnel at several client-side packet-loss levels, once
// over the QUIC transport and once over the port-forwarded gRPC transport, and
// compares the per-stream completion-time tail.
//
// The hypothesis: on the gRPC transport every tunneled flow shares one HTTP/2
// connection, so a single lost packet stalls unrelated flows (head-of-line
// blocking) and the p99 balloons as loss rises; on QUIC each flow is an
// independently retransmitted stream, so the tail stays close to the median.
// The test asserts that at the highest loss level the gRPC p99 is at least
// exp1MinP99Ratio times the QUIC p99, and writes the full p50/p95/p99 table to
// perf/results/experiment1.csv regardless.
//
// See perf/README.md. This is skipped unless a QUIC-reachable endpoint is
// configured (PERF_QUIC_EXTERNAL_HOST); without loss injection (no
// PERF_NETEM_IFACE) it still runs and records a clean-network baseline, which
// is expected to show little difference -- the point of the experiment is the
// behavior under loss.
func TestExperiment1_HeadOfLineBlockingUnderLoss(t *testing.T) {
	cfg := loadConfig(t)
	if cfg.quicExternalHost == "" {
		t.Skip("set PERF_QUIC_EXTERNAL_HOST (and PERF_QUIC_NODEPORT) to the forwarder's reachable address to run the quic arm")
	}
	if cfg.netemIface == "" {
		t.Log("PERF_NETEM_IFACE not set: running a clean-network baseline only; " +
			"the head-of-line result requires loss injection on the transport interface")
	}

	ts := time.Now().UTC().Format(time.RFC3339)
	var rows [][]string
	// p99 at the top loss level, per transport, for the final ratio assertion.
	topLoss := exp1LossPercents[len(exp1LossPercents)-1]
	p99AtTopLoss := map[transport]time.Duration{}

	for _, tr := range []transport{transportQUIC, transportGRPC} {
		func() {
			cfg.quit(t)
			cfg.helmUninstall(t)
			cfg.helmInstall(t, tr)
			t.Cleanup(func() { cfg.quit(t); cfg.helmUninstall(t) })

			run(t, "kubectl", "-n", cfg.appNamespace, "apply", "-f", "testdata/payload.yaml")
			// 240s: on Autopilot the first schedule may provision a node.
			run(t, "kubectl", "-n", cfg.appNamespace, "rollout", "status",
				"deploy/perf-payload", "--timeout=240s")
			t.Cleanup(func() {
				_ = runQuiet("kubectl", "-n", cfg.appNamespace, "delete", "-f", "testdata/payload.yaml")
			})

			cfg.connect(t)
			cfg.assertTransport(t, tr)

			for _, loss := range exp1LossPercents {
				removeLoss := cfg.applyLoss(t, loss)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				results := runConcurrentDownloads(ctx, exp1Streams)
				cancel()
				removeLoss()

				p := summarize(results)
				t.Logf("%s @ %g%% loss: p50=%v p95=%v p99=%v (failures=%d/%d)",
					tr, loss, p.p50, p.p95, p.p99, p.failures, exp1Streams)
				rows = append(rows, msRow(ts, tr, loss, exp1Streams, exp1PayloadBytes, p))
				if loss == topLoss {
					p99AtTopLoss[tr] = p.p99
				}
			}
		}()
	}

	path := writeCSV(t, cfg.outDir, "experiment1", rows)
	t.Logf("results written to %s", path)

	// The head-of-line assertion only makes sense when loss was actually
	// injected; a clean-network baseline is recorded but not asserted.
	if cfg.netemIface == "" || topLoss == 0 {
		return
	}
	quicP99 := p99AtTopLoss[transportQUIC]
	grpcP99 := p99AtTopLoss[transportGRPC]
	if quicP99 <= 0 {
		t.Fatalf("no successful QUIC downloads at %g%% loss; cannot compare", topLoss)
	}
	ratio := float64(grpcP99) / float64(quicP99)
	t.Logf("at %g%% loss: gRPC p99 / QUIC p99 = %.2f (threshold %.2f)", topLoss, ratio, exp1MinP99Ratio)
	if ratio < exp1MinP99Ratio {
		t.Fatalf("expected gRPC p99 to be >= %.2fx QUIC p99 at %g%% loss (head-of-line blocking), got %.2fx",
			exp1MinP99Ratio, topLoss, ratio)
	}
}
