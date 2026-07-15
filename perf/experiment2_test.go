//go:build perf

package perf

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Experiment 2 knobs. Kept as vars (not env) because they define the shape of the published
// result; override in a fork if you are exploring, not per run. Mirrors experiment 1's knobs
// in name and starting values -- see exp1Workers/exp1RequestBytes/exp1ThinkTime in
// experiment1_test.go for the fuller derivation -- but the mechanism under test is
// different, so the reasoning below is specific to it.
var (
	exp2Workers      = 10 // concurrent HTTP/3 requests, ALL sharing one inner QUIC connection
	exp2RequestBytes = 8 * 1024
	exp2ThinkTime    = 300 * time.Millisecond
	exp2WindowDur    = 30 * time.Second
	exp2LossPercents = []float64{0, 1, 3}
	// exp2MinP95Ratio is the pass threshold at the highest loss level: the stream-carriage
	// arm's p95 request latency must be at least this many times the datagram-carriage arm's
	// p95. Deliberately more conservative than exp1MinP95Ratio (1.4) as a starting point --
	// this experiment's HoL mechanism is a single shared inner QUIC connection's OWN loss
	// recovery being either exposed to the tunnel's real per-packet loss (datagram carriage)
	// or hidden behind an outer reliable stream's coarser, connection-wide stall (stream
	// carriage), rather than exp1's cross-flow blocking on a shared byte stream, and it was
	// unmeasured at authoring time. Raise it if a run shows a larger stable gap; if a run
	// shows none, that is the recorded result, not a reason to lower this until one appears.
	exp2MinP95Ratio = 1.3
)

// TestExperiment2_DatagramHeadOfLine runs exp2Workers concurrent HTTP/3 requests -- ALL
// sharing a single inner QUIC connection (one *http.Client, one http3.Transport; see
// newH3Client/runH3Workers) to the in-cluster h3 server (testdata/h3server) -- through the
// telepresence tunnel, at several data-path packet-loss levels, once with the manager's QUIC
// tunnel offering RFC 9221 datagram carriage for tunneled UDP and once with it forced off
// (TELEPRESENCE_QUIC_DISABLE_DATAGRAMS), and compares the per-request latency tail.
//
// Both arms run over the QUIC tunnel transport (helmInstall is always called with
// transportQUIC); what differs is how that transport carries the ONE UDP flow the shared
// inner QUIC connection produces (one client source port -> one ConnID; see
// pkg/tunnel/datagram.go). That is the deliberate difference from experiment 1, which
// compares two different transports (QUIC vs. gRPC) each carrying MANY independent flows.
// Here there is exactly one flow, and the question is how its own messages are serialized:
//
//   - Stream carriage sends every message of that one flow, in order, on the flow's own
//     outer QUIC stream. The stream is reliable, so no inner UDP packet is ever truly lost --
//     but a lost carrier packet stalls delivery of every message queued behind it on that
//     stream until the retransmit lands (an outer RTT), and since every one of the
//     exp2Workers HTTP/3 requests is multiplexed as an inner QUIC stream on the SAME shared
//     inner connection, all of them stall together, even though HTTP/3 itself has no
//     dependency between those streams.
//   - Datagram carriage sends each message as an independent, unreliable outer QUIC
//     datagram. A lost carrier packet now genuinely loses exactly the one inner UDP packet it
//     carried -- ordinary UDP semantics -- which the inner QUIC connection's OWN per-stream
//     loss recovery handles, delaying only the one or two inner HTTP/3 streams whose data was
//     actually in it.
//
// The hypothesis: forcing stream carriage converts the inner protocol's own designed-for-loss
// behavior into connection-wide stalls, so the stream-carriage arm's tail latency should be
// worse than the datagram-carriage arm's, in proportion to how many concurrent inner streams
// are usually in flight when a loss lands. The test asserts that at the highest loss level
// the stream-carriage p95 is at least exp2MinP95Ratio times the datagram-carriage p95, and
// writes the full p50/p95/p99 table to perf/results/experiment2.csv regardless.
//
// Skipped unless a QUIC-reachable endpoint is configured (PERF_QUIC_EXTERNAL_HOST); without
// any impairment configured it still runs and records a clean-network baseline. The loss
// must be on the DATA path (PERF_IMPAIR_NODE against a kind node; see perf/README.md) for the
// same reason experiment 1 needs it: client-egress-only loss degrades ACKs and requests but
// cannot touch response data.
func TestExperiment2_DatagramHeadOfLine(t *testing.T) {
	cfg := loadConfig(t)
	if cfg.quicExternalHost == "" {
		t.Skip("set PERF_QUIC_EXTERNAL_HOST (and PERF_QUIC_NODEPORT) to the forwarder's reachable address to run this experiment")
	}
	if !cfg.impairing() {
		t.Log("neither PERF_IMPAIR_NODE nor PERF_NETEM_IFACE is set: running a " +
			"clean-network baseline only; the head-of-line result requires loss on " +
			"the download data path (PERF_IMPAIR_NODE against a kind node)")
	}

	ts := time.Now().UTC().Format(time.RFC3339)
	var rows [][]string
	topLoss := exp2LossPercents[len(exp2LossPercents)-1]
	p95AtTopLoss := map[transport]time.Duration{}

	for _, arm := range []transport{armDatagram, armStream} {
		func() {
			cfg.quit(t)
			cfg.helmUninstall(t)
			cfg.helmInstall(t, transportQUIC)
			if arm == armStream {
				cfg.setDatagramsDisabled(t, true)
			}
			t.Cleanup(func() { cfg.quit(t); cfg.helmUninstall(t) })

			cfg.kubectl(t, "-n", cfg.appNamespace, "apply", "-f", "testdata/h3server.yaml")
			// 240s: on Autopilot the first schedule may provision a node.
			cfg.kubectl(t, "-n", cfg.appNamespace, "rollout", "status",
				"deploy/perf-h3", "--timeout=240s")
			t.Cleanup(func() {
				_ = cfg.kubectlQuiet("-n", cfg.appNamespace, "delete", "-f", "testdata/h3server.yaml")
			})

			cfg.connect(t)
			cfg.assertTransport(t, transportQUIC)

			client, closeClient := newH3Client()
			defer closeClient()
			cfg.warmupH3(t, client, exp2Workers, exp2RequestBytes)

			for _, loss := range exp2LossPercents {
				removeLoss := cfg.applyLoss(t, loss)
				ctx, cancel := context.WithTimeout(context.Background(), exp2WindowDur+time.Minute)
				results := runH3Workers(ctx, client, h3PayloadURL, exp2Workers, exp2RequestBytes, exp2ThinkTime, exp2WindowDur)
				cancel()
				removeLoss()

				p := summarize(results)
				t.Logf("%s @ %g%% loss: %d requests, p50=%v p95=%v p99=%v (failures=%d)",
					arm, loss, p.n, p.p50, p.p95, p.p99, p.failures)
				if p.failures > 0 {
					logFailures(t, results)
				}
				rows = append(rows, msRow(ts, arm, loss, exp2Workers, exp2RequestBytes, p))
				if loss == topLoss {
					p95AtTopLoss[arm] = p.p95
				}
			}
		}()
	}

	path := writeCSV(t, cfg.outDir, "experiment2", rows)
	t.Logf("results written to %s", path)

	// As in experiment 1, the ratio assertion only makes sense when loss was actually
	// injected on the download data path.
	if cfg.impairNode == "" || topLoss == 0 {
		return
	}
	datagramP95 := p95AtTopLoss[armDatagram]
	streamP95 := p95AtTopLoss[armStream]
	if datagramP95 <= 0 {
		t.Fatalf("no successful datagram-carriage requests at %g%% loss; cannot compare", topLoss)
	}
	ratio := float64(streamP95) / float64(datagramP95)
	t.Logf("at %g%% loss: stream p95 / datagram p95 = %.2f (threshold %.2f)", topLoss, ratio, exp2MinP95Ratio)
	if ratio < exp2MinP95Ratio {
		t.Fatalf("expected stream-carriage p95 to be >= %.2fx datagram-carriage p95 at %g%% loss (datagram head-of-line benefit), got %.2fx",
			exp2MinP95Ratio, topLoss, ratio)
	}
}

// armDatagram and armStream label experiment 2's two arms for logging and CSV output. Both
// install the QUIC tunnel transport (helmInstall is always called with transportQUIC); these
// reuse the harness's `transport` string type purely as a label, never as a helmInstall
// selector, so the CSV's "transport" column reads "quic-datagram" / "quic-stream" instead of
// a bare "quic" that would leave the two rows indistinguishable.
const (
	armDatagram transport = "quic-datagram"
	armStream   transport = "quic-stream"
)

// setDatagramsDisabled sets TELEPRESENCE_QUIC_DISABLE_DATAGRAMS on the traffic-manager
// deployment and waits for the rollout, the same way helmInstall applies QUIC_GO_DISABLE_GSO
// in PERF_IMPAIR_NODE mode. Only ever called with true (to produce the stream-carriage arm);
// the datagram-carriage arm is a fresh install with the variable simply never set, so its
// deployment never carries the extra rollout this causes.
func (c config) setDatagramsDisabled(t *testing.T, disabled bool) {
	t.Helper()
	c.kubectl(t, "-n", c.managerNamespace, "set", "env",
		"deploy/traffic-manager", fmt.Sprintf("TELEPRESENCE_QUIC_DISABLE_DATAGRAMS=%t", disabled))
	c.kubectl(t, "-n", c.managerNamespace, "rollout", "status",
		"deploy/traffic-manager", "--timeout=120s")
}
