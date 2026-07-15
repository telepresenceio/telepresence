//go:build perf

package network

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Datagram-carriage experiment knobs. Kept as vars (not env) because they define the shape
// of the published result; override in a fork if you are exploring, not per run. Mirrors the
// head-of-line experiment's knobs in name and starting values -- see holWorkers/
// holRequestBytes/holThinkTime in headofline_test.go for the fuller derivation -- but the
// mechanism under test is different, so the reasoning below is specific to it.
var (
	datagramWorkers      = 10 // concurrent HTTP/3 requests, ALL sharing one inner QUIC connection
	datagramRequestBytes = 8 * 1024
	datagramThinkTime    = 300 * time.Millisecond
	datagramWindowDur    = 30 * time.Second
	datagramLossPercents = []float64{0, 1, 3}
)

// TestDatagramCarriageUnderLoss runs datagramWorkers concurrent HTTP/3 requests -- ALL
// sharing a single inner QUIC connection (one *http.Client, one http3.Transport; see
// newH3Client/runH3Workers) to the in-cluster h3 server (testdata/h3server) -- through the
// telepresence tunnel, at several data-path packet-loss levels, once with the manager's QUIC
// tunnel opting into RFC 9221 datagram carriage for tunneled UDP (TELEPRESENCE_QUIC_ENABLE_DATAGRAMS)
// and once with it left at the default of stream carriage, and compares the per-request latency tail.
//
// Both arms run over the QUIC tunnel transport (helmInstall is always called with
// transportQUIC); what differs is how that transport carries the ONE UDP flow the shared
// inner QUIC connection produces (one client source port -> one ConnID; see
// pkg/tunnel/datagram.go). That is the deliberate difference from the head-of-line
// experiment, which compares two different transports (QUIC vs. gRPC) each carrying MANY
// independent flows. Here there is exactly one flow, and the question is how its own messages
// are serialized:
//
//   - Stream carriage sends every message of that one flow, in order, on the flow's own
//     outer QUIC stream. The stream is reliable, so no inner UDP packet is ever truly lost --
//     but a lost carrier packet stalls delivery of every message queued behind it on that
//     stream until the retransmit lands (an outer RTT), and since every one of the
//     datagramWorkers HTTP/3 requests is multiplexed as an inner QUIC stream on the SAME
//     shared inner connection, all of them stall together, even though HTTP/3 itself has no
//     dependency between those streams.
//   - Datagram carriage sends each message as an independent, unreliable outer QUIC
//     datagram. A lost carrier packet now genuinely loses exactly the one inner UDP packet it
//     carried -- ordinary UDP semantics -- which the inner QUIC connection's OWN per-stream
//     loss recovery handles, delaying only the one or two inner HTTP/3 streams whose data was
//     actually in it.
//
// The hypothesis was that forcing stream carriage would convert the inner protocol's own
// designed-for-loss behavior into connection-wide stalls, making the stream-carriage arm's
// tail latency worse than the datagram-carriage arm's. MEASUREMENT DID NOT BEAR THIS OUT.
// Across three runs on kind (recorded in perf/README.md, "Datagram carriage"), datagram
// carriage was never better and was often substantially worse -- including a large p95
// penalty at 0% loss, where there is no loss for the hypothesized mechanism to act on. The
// most plausible reason is that this is QUIC-in-QUIC: with stream carriage the outer reliable
// stream recovers a lost carrier packet over the short client<->forwarder<->manager hop,
// whereas datagram carriage forces the INNER QUIC to recover it over the full end-to-end
// path, which is slower -- so the "unreliable is faster under loss" intuition inverts here.
// This test therefore no longer asserts a benefit; it records the p50/p95/p99 comparison to
// perf/results/datagram-carriage.csv and logs the observed ratio, so the result is
// reproducible and visible without pretending to a win the data does not show.
//
// Skipped unless a QUIC-reachable endpoint is configured (PERF_QUIC_EXTERNAL_HOST); without
// any impairment configured it still runs and records a clean-network baseline. The loss
// must be on the DATA path (PERF_IMPAIR_NODE against a kind node; see perf/README.md) for the
// same reason the head-of-line experiment needs it: client-egress-only loss degrades ACKs and
// requests but cannot touch response data.
func TestDatagramCarriageUnderLoss(t *testing.T) {
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
	topLoss := datagramLossPercents[len(datagramLossPercents)-1]
	p95AtTopLoss := map[transport]time.Duration{}

	for _, arm := range []transport{armDatagram, armStream} {
		func() {
			cfg.quit(t)
			cfg.helmUninstall(t)
			cfg.helmInstall(t, transportQUIC)
			if arm == armDatagram {
				cfg.setDatagramsEnabled(t, true)
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
			cfg.warmupH3(t, client, datagramWorkers, datagramRequestBytes)

			for _, loss := range datagramLossPercents {
				removeLoss := cfg.applyLoss(t, loss)
				ctx, cancel := context.WithTimeout(context.Background(), datagramWindowDur+time.Minute)
				results := runH3Workers(ctx, client, h3PayloadURL, datagramWorkers, datagramRequestBytes, datagramThinkTime, datagramWindowDur)
				cancel()
				removeLoss()

				p := summarize(results)
				t.Logf("%s @ %g%% loss: %d requests, p50=%v p95=%v p99=%v (failures=%d)",
					arm, loss, p.n, p.p50, p.p95, p.p99, p.failures)
				if p.failures > 0 {
					logFailures(t, results)
				}
				rows = append(rows, msRow(ts, arm, loss, datagramWorkers, datagramRequestBytes, p))
				if loss == topLoss {
					p95AtTopLoss[arm] = p.p95
				}
			}
		}()
	}

	path := writeCSV(t, cfg.outDir, "datagram-carriage", rows)
	t.Logf("results written to %s", path)

	// This experiment records rather than asserts: the hypothesized datagram benefit did not
	// materialize (see the doc comment above and perf/README.md), so there is no benefit to
	// gate on. When loss was injected, log the observed stream/datagram p95 ratio so a run's
	// direction is visible in the output; a value below 1 means datagram carriage was the worse
	// of the two, which is what has been measured so far.
	if cfg.impairNode == "" || topLoss == 0 {
		return
	}
	datagramP95 := p95AtTopLoss[armDatagram]
	streamP95 := p95AtTopLoss[armStream]
	if datagramP95 <= 0 {
		t.Logf("no successful datagram-carriage requests at %g%% loss; nothing to compare", topLoss)
		return
	}
	t.Logf("at %g%% loss: stream p95 / datagram p95 = %.2f (a value < 1 means datagram carriage was worse)",
		topLoss, float64(streamP95)/float64(datagramP95))
}

// armDatagram and armStream label this experiment's two arms for logging and CSV output. Both
// install the QUIC tunnel transport (helmInstall is always called with transportQUIC); these
// reuse the harness's `transport` string type purely as a label, never as a helmInstall
// selector, so the CSV's "transport" column reads "quic-datagram" / "quic-stream" instead of
// a bare "quic" that would leave the two rows indistinguishable.
const (
	armDatagram transport = "quic-datagram"
	armStream   transport = "quic-stream"
)

// setDatagramsEnabled sets TELEPRESENCE_QUIC_ENABLE_DATAGRAMS on the traffic-manager
// deployment and waits for the rollout, the same way helmInstall applies QUIC_GO_DISABLE_GSO
// in PERF_IMPAIR_NODE mode. RFC 9221 datagram carriage is opt-in and off by default, so this
// is only ever called with true (to produce the datagram-carriage arm); the stream-carriage
// arm is a fresh install with the variable simply never set, so its deployment never carries
// the extra rollout this causes.
func (c config) setDatagramsEnabled(t *testing.T, enabled bool) {
	t.Helper()
	c.kubectl(t, "-n", c.managerNamespace, "set", "env",
		"deploy/traffic-manager", fmt.Sprintf("TELEPRESENCE_QUIC_ENABLE_DATAGRAMS=%t", enabled))
	c.kubectl(t, "-n", c.managerNamespace, "rollout", "status",
		"deploy/traffic-manager", "--timeout=120s")
}
