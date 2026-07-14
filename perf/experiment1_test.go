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
	exp1Workers = 10 // concurrent request workers, one long-lived flow each
	// exp1RequestBytes is the size of each request (a Range read of the payload
	// object). It must be small enough to fit comfortably inside a congestion
	// window: a request that spans many round trips turns into a bulk transfer
	// whose completion time is congestion-control-bound (the Mathis limit,
	// ~MSS/RTT * 1.22/sqrt(loss)), which drowns the head-of-line signal this
	// experiment exists to measure.
	exp1RequestBytes = 8 * 1024
	// exp1ThinkTime is each worker's pause between requests. Together with
	// exp1Workers and exp1RequestBytes it sets the offered load, which must stay
	// well BELOW the shared connection's loss-limited capacity at the top loss
	// level: on a saturated connection every request queues behind the
	// congestion window and the experiment degenerates into measuring
	// congestion-control efficiency again (a 50-worker variant of this
	// experiment did exactly that -- both arms' medians moved into seconds and
	// the head-of-line signal drowned). On a mostly idle connection the only
	// delays are the recovery events themselves: on the shared gRPC byte stream
	// a single loss shows up in unrelated workers' latencies, over QUIC it
	// stays confined to the punctured stream.
	exp1ThinkTime = 300 * time.Millisecond
	// exp1WindowDur is how long each (transport, loss) window runs; every worker
	// issues sequential requests for the whole window.
	exp1WindowDur    = 30 * time.Second
	exp1LossPercents = []float64{0, 1, 3}
	// exp1MinP95Ratio is the pass threshold at the highest loss level: the gRPC
	// transport's p95 request latency must be at least this many times the QUIC
	// transport's p95, i.e. QUIC's independently recovered streams must
	// demonstrably contain the tail that head-of-line blocking on the shared
	// gRPC connection inflates. A relative threshold (not an absolute latency)
	// is what makes the assertion portable across clusters and networks; p95
	// rather than p99 because at these window sizes (~900 samples) p99 rests on
	// a handful of samples. Measured on kind at 20ms RTT: 1.75x at 3% loss
	// (1.8x at 1%), with the medians telling the same story -- at 1% loss the
	// QUIC median is indistinguishable from the clean baseline while the gRPC
	// median doubles.
	exp1MinP95Ratio = 1.4
)

// TestExperiment1_HeadOfLineBlockingUnderLoss runs exp1Workers concurrent
// workers, each issuing sequential small (exp1RequestBytes) requests over its
// own persistent connection through the tunnel, at several data-path
// packet-loss levels -- once over the QUIC transport and once over the
// port-forwarded gRPC transport -- and compares the per-request latency tail.
//
// The hypothesis: on the gRPC transport every tunneled flow shares one TCP
// byte stream, so a single lost packet stalls in-order delivery for all of
// them until the retransmit lands, and unrelated requests eat that wait in
// their p99; on QUIC each flow is an independently retransmitted stream, so a
// loss delays only the punctured request and the tail stays close to the
// median. The test asserts that at the highest loss level the gRPC p95 is at
// least exp1MinP95Ratio times the QUIC p95, and writes the full p50/p95/p99
// table to perf/results/experiment1.csv regardless.
//
// When exploring other RTTs (PERF_NETEM_DELAY), scale exp1ThinkTime with the
// RTT: the Mathis capacity the offered load must stay below is proportional
// to 1/RTT, so knobs tuned for 20ms saturate the connection at 80ms and the
// experiment silently degenerates into the queue-bound regime again.
//
// The loss must be on the DATA path (PERF_IMPAIR_NODE against a kind node;
// see perf/README.md) -- client-egress loss only degrades ACKs and requests
// and cannot stall response data. See perf/README.md for the setup. This is
// skipped unless a QUIC-reachable endpoint is configured
// (PERF_QUIC_EXTERNAL_HOST); without any impairment configured it still runs
// and records a clean-network baseline.
func TestExperiment1_HeadOfLineBlockingUnderLoss(t *testing.T) {
	cfg := loadConfig(t)
	if cfg.quicExternalHost == "" {
		t.Skip("set PERF_QUIC_EXTERNAL_HOST (and PERF_QUIC_NODEPORT) to the forwarder's reachable address to run the quic arm")
	}
	if !cfg.impairing() {
		t.Log("neither PERF_IMPAIR_NODE nor PERF_NETEM_IFACE is set: running a " +
			"clean-network baseline only; the head-of-line result requires loss on " +
			"the download data path (PERF_IMPAIR_NODE against a kind node)")
	}

	ts := time.Now().UTC().Format(time.RFC3339)
	var rows [][]string
	// p95 at the top loss level, per transport, for the final ratio assertion.
	topLoss := exp1LossPercents[len(exp1LossPercents)-1]
	p95AtTopLoss := map[transport]time.Duration{}

	for _, tr := range []transport{transportQUIC, transportGRPC} {
		func() {
			cfg.quit(t)
			cfg.helmUninstall(t)
			cfg.helmInstall(t, tr)
			t.Cleanup(func() { cfg.quit(t); cfg.helmUninstall(t) })

			cfg.kubectl(t, "-n", cfg.appNamespace, "apply", "-f", "testdata/payload.yaml")
			// 240s: on Autopilot the first schedule may provision a node.
			cfg.kubectl(t, "-n", cfg.appNamespace, "rollout", "status",
				"deploy/perf-payload", "--timeout=240s")
			t.Cleanup(func() {
				_ = cfg.kubectlQuiet("-n", cfg.appNamespace, "delete", "-f", "testdata/payload.yaml")
			})

			cfg.connect(t)
			cfg.assertTransport(t, tr)
			cfg.warmup(t, exp1Workers, exp1RequestBytes)

			for _, loss := range exp1LossPercents {
				removeLoss := cfg.applyLoss(t, loss)
				ctx, cancel := context.WithTimeout(context.Background(), exp1WindowDur+time.Minute)
				results := runRequestWorkers(ctx, exp1Workers, exp1RequestBytes, exp1ThinkTime, exp1WindowDur)
				cancel()
				removeLoss()

				p := summarize(results)
				t.Logf("%s @ %g%% loss: %d requests, p50=%v p95=%v p99=%v (failures=%d)",
					tr, loss, p.n, p.p50, p.p95, p.p99, p.failures)
				if p.failures > 0 {
					logFailures(t, results)
				}
				rows = append(rows, msRow(ts, tr, loss, exp1Workers, exp1RequestBytes, p))
				if loss == topLoss {
					p95AtTopLoss[tr] = p.p95
				}
			}
		}()
	}

	path := writeCSV(t, cfg.outDir, "experiment1", rows)
	t.Logf("results written to %s", path)

	// The head-of-line assertion only makes sense when loss was actually injected
	// on the download data path: egress-only loss (PERF_NETEM_IFACE) degrades ACKs
	// and requests but never stalls response data, so a baseline or egress-only
	// run is recorded without being asserted.
	if cfg.impairNode == "" || topLoss == 0 {
		return
	}
	quicP95 := p95AtTopLoss[transportQUIC]
	grpcP95 := p95AtTopLoss[transportGRPC]
	if quicP95 <= 0 {
		t.Fatalf("no successful QUIC requests at %g%% loss; cannot compare", topLoss)
	}
	ratio := float64(grpcP95) / float64(quicP95)
	t.Logf("at %g%% loss: gRPC p95 / QUIC p95 = %.2f (threshold %.2f)", topLoss, ratio, exp1MinP95Ratio)
	if ratio < exp1MinP95Ratio {
		t.Fatalf("expected gRPC p95 to be >= %.2fx QUIC p95 at %g%% loss (head-of-line blocking), got %.2fx",
			exp1MinP95Ratio, topLoss, ratio)
	}
}
