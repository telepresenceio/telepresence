//go:build perf

package perf

import (
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// config is the environment-driven configuration shared by the perf
// experiments. Everything has a default so the harness runs against the local
// kind-dev cluster with no setup, but every knob that varies by environment --
// the telepresence binary, the cluster's QUIC-reachable address, and the
// interface `tc` should impair -- is overridable.
type config struct {
	// telepresence is the client binary under test.
	telepresence string
	// managerNamespace is where the traffic-manager is installed.
	managerNamespace string
	// appNamespace is where the perf workload runs.
	appNamespace string
	// chartDir is the Helm chart used to (re)install the traffic-manager.
	chartDir string
	// image registry/tag/pullPolicy for a locally built manager image.
	imageRegistry   string
	imageTag        string
	imagePullPolicy string

	// quicExternalHost/Port is the externally reachable QUIC endpoint the
	// manager advertises to clients (the forwarder's Service). Required for the
	// quic arm; if empty, the quic arm is skipped with a clear message.
	quicExternalHost string
	quicNodePort     int

	// netemIface is the host interface `tc qdisc ... netem` is applied to. This
	// must be the interface that carries tunnel transport packets toward the
	// cluster (NOT the telepresence VIF, which carries decapsulated traffic).
	// Empty disables loss injection (a clean-network baseline run).
	netemIface string

	// outDir is where CSV results are written.
	outDir string
}

func loadConfig(t *testing.T) config {
	t.Helper()
	repoRoot := repoRoot(t)
	c := config{
		telepresence:     envOr("PERF_TELEPRESENCE", filepath.Join(repoRoot, "build-output", "bin", "telepresence")),
		managerNamespace: envOr("PERF_MANAGER_NAMESPACE", "ambassador"),
		appNamespace:     envOr("PERF_APP_NAMESPACE", "default"),
		chartDir:         envOr("PERF_CHART_DIR", filepath.Join(repoRoot, "charts", "telepresence-oss")),
		imageRegistry:    envOr("PERF_IMAGE_REGISTRY", "local"),
		imageTag:         envOr("PERF_IMAGE_TAG", ""),
		imagePullPolicy:  envOr("PERF_IMAGE_PULL_POLICY", "Never"),
		quicExternalHost: os.Getenv("PERF_QUIC_EXTERNAL_HOST"),
		quicNodePort:     envInt("PERF_QUIC_NODEPORT", 30777),
		netemIface:       os.Getenv("PERF_NETEM_IFACE"),
		outDir:           envOr("PERF_OUT_DIR", filepath.Join(repoRoot, "perf", "results")),
	}
	if c.imageTag == "" {
		// Match the version the local binary reports, so a `local` image built
		// from this tree is the one that gets installed.
		c.imageTag = strings.TrimPrefix(clientVersion(t, c.telepresence), "v")
	}
	return c
}

// transport is one arm of an experiment: the manager is installed with QUIC
// either enabled (and reachable) or disabled, and the same workload is driven
// over whichever tunnel transport results.
type transport string

const (
	transportQUIC transport = "quic"
	transportGRPC transport = "grpc"
)

// helmInstall installs the traffic-manager for the given transport arm. For the
// quic arm it enables the forwarder and advertises quicExternalHost:quicNodePort.
func (c config) helmInstall(t *testing.T, tr transport) {
	t.Helper()
	args := []string{
		"helm", "install", "--set", "image.registry=" + c.imageRegistry,
		"--set", "image.tag=" + c.imageTag,
		"--set", "image.pullPolicy=" + c.imagePullPolicy,
	}
	if tr == transportQUIC {
		args = append(args,
			"--set", "quicTunnel.enabled=true",
			"--set", "quicTunnel.service.type=NodePort",
			"--set", fmt.Sprintf("quicTunnel.service.nodePort=%d", c.quicNodePort),
			"--set", "quicTunnel.externalHost="+c.quicExternalHost,
			"--set", fmt.Sprintf("quicTunnel.externalPort=%d", c.quicNodePort),
		)
	}
	run(t, c.telepresence, args...)
	run(t, "kubectl", "-n", c.managerNamespace, "rollout", "status",
		"deploy/traffic-manager", "--timeout=120s")
	if tr == transportQUIC {
		run(t, "kubectl", "-n", c.managerNamespace, "rollout", "status",
			"deploy/quic-forwarder", "--timeout=120s")
	}
}

func (c config) helmUninstall(t *testing.T) {
	t.Helper()
	// Best-effort: the arm may have failed before installing.
	_ = exec.Command(c.telepresence, "helm", "uninstall").Run()
}

func (c config) connect(t *testing.T) {
	t.Helper()
	run(t, c.telepresence, "connect", "--namespace", c.appNamespace)
}

func (c config) quit(t *testing.T) {
	t.Helper()
	_ = exec.Command(c.telepresence, "quit", "-s").Run()
}

// assertTransport ensures the session is on the expected tunnel transport,
// reconnecting until it is, so a quic arm that silently fell back to gRPC is
// caught rather than mislabeled in the results. The QUIC dial happens once per
// connect with a short budget (quicDialTimeout, 3s), which a cold handshake over
// a WAN can miss; each reconnect is a fresh dial, so a transient miss does not
// fail the arm. The grpc arm (QUIC disabled) satisfies the first check
// immediately and never reconnects.
func (c config) assertTransport(t *testing.T, want transport) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	var got string
	for {
		out, _ := exec.Command(c.telepresence, "status", "--output", "json").Output()
		got = parseTunnelTransport(out)
		switch {
		case want == transportQUIC && strings.HasPrefix(got, "quic "):
			return
		case want == transportGRPC && got == "grpc":
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected tunnel transport %q, but status reported %q after repeated reconnects", want, got)
		}
		// A fresh session gets a fresh QUIC dial.
		c.quit(t)
		c.connect(t)
		time.Sleep(2 * time.Second)
	}
}

// applyLoss adds an egress netem qdisc dropping lossPct% of packets on the
// configured interface, and returns a cleanup that removes it. A no-op (with a
// no-op cleanup) when no interface is configured.
//
// Segmentation offloads are disabled for the duration of the loss window: with
// TSO/GSO on, netem at the root qdisc sits above the segmentation step and each
// "packet" it drops is a pre-segmentation super-packet of up to 64 KB of TCP
// data, while QUIC's datagrams are already wire-sized -- so at the same
// configured rate the TCP arm loses vastly more data per drop event and the
// arms are not comparable. With offloads off, both arms lose wire-sized packets
// at the configured rate.
func (c config) applyLoss(t *testing.T, lossPct float64) func() {
	t.Helper()
	if c.netemIface == "" || lossPct <= 0 {
		return func() {}
	}
	restoreOffloads := disableSegmentationOffloads(t, c.netemIface)
	run(t, "sudo", "tc", "qdisc", "add", "dev", c.netemIface, "root",
		"netem", "loss", fmt.Sprintf("%g%%", lossPct))
	del := func() {
		_ = exec.Command("sudo", "tc", "qdisc", "del", "dev", c.netemIface, "root").Run()
		restoreOffloads()
	}
	// Safety net: even if the caller never invokes the returned cleanup (a panic
	// or a fatal mid-window), the qdisc must not be left impairing the real
	// interface. tc qdisc del is idempotent, so calling it twice is harmless.
	t.Cleanup(del)
	return del
}

// offloadFlags maps the feature names `ethtool -k` reports to the short flags
// `ethtool -K` sets. Only transmit-side segmentation offloads matter here: loss
// is injected on egress only, and receive-side coalescing (GRO/LRO) happens
// below any ingress impairment point anyway.
var offloadFlags = map[string]string{
	"tcp-segmentation-offload":     "tso",
	"generic-segmentation-offload": "gso",
}

// disableSegmentationOffloads turns off the transmit segmentation offloads that
// are currently enabled (and changeable) on iface and returns a func restoring
// exactly those, so a host where an offload was already off is left untouched.
func disableSegmentationOffloads(t *testing.T, iface string) func() {
	t.Helper()
	out, err := exec.Command("ethtool", "-k", iface).Output()
	if err != nil {
		t.Fatalf("ethtool -k %s: %v", iface, err)
	}
	var toRestore []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		// "[fixed]" features have a third field and cannot be changed.
		if len(f) != 2 || f[1] != "on" {
			continue
		}
		if flag, ok := offloadFlags[strings.TrimSuffix(f[0], ":")]; ok {
			toRestore = append(toRestore, flag)
			run(t, "sudo", "ethtool", "-K", iface, flag, "off")
		}
	}
	return func() {
		for _, flag := range toRestore {
			_ = exec.Command("sudo", "ethtool", "-K", iface, flag, "on").Run()
		}
	}
}

// streamResult is one download's outcome.
type streamResult struct {
	duration time.Duration
	err      error
}

// warmup drives the payload path until it flows: first single downloads with
// retries (a fresh session's DNS and tunnel may lag connect), then one full
// concurrent window whose timings are discarded. Without this, session
// establishment, nginx page-cache population, and congestion-control ramp-up
// all land in the first measured window and make it incomparable to the later
// ones.
func (c config) warmup(t *testing.T, streams int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		r := timedGet(ctx, payloadURL)
		cancel()
		if r.err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("warmup: payload never became reachable through the tunnel: %v", r.err)
		}
		t.Logf("warmup: %v; retrying", r.err)
		time.Sleep(2 * time.Second)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	results := runConcurrentDownloads(ctx, streams)
	if p := summarize(results); p.failures > 0 {
		t.Logf("warmup window: %d/%d streams failed", p.failures, streams)
		logFailures(t, results)
	}
}

// logFailures logs the distinct error strings (with multiplicities) from a
// window's failed streams, so a failed arm is diagnosable from the test log
// instead of showing up only as a failure count.
func logFailures(t *testing.T, results []streamResult) {
	t.Helper()
	counts := map[string]int{}
	for _, r := range results {
		if r.err != nil {
			counts[r.err.Error()]++
		}
	}
	msgs := make([]string, 0, len(counts))
	for m := range counts {
		msgs = append(msgs, m)
	}
	sort.Slice(msgs, func(i, j int) bool { return counts[msgs[i]] > counts[msgs[j]] })
	for i, m := range msgs {
		if i == 3 {
			t.Logf("  ... and %d more distinct errors", len(msgs)-3)
			break
		}
		t.Logf("  failure x%d: %s", counts[m], m)
	}
}

// payloadURL is the fixed static asset served by the perf-payload workload
// (testdata/payload.yaml); its size is set by that manifest's initContainer and
// must match exp1PayloadBytes.
const payloadURL = "http://perf-payload/payload.bin"

// runConcurrentDownloads issues `streams` concurrent GETs of the payload from
// the perf workload through the tunnel, timing each, and returns the per-stream
// results. The URL is resolved via the cluster DNS name, so the traffic rides
// the VPN (manager tunnel) -- exactly the shared transport whose head-of-line
// behavior the experiment measures.
func runConcurrentDownloads(ctx context.Context, streams int) []streamResult {
	results := make([]streamResult, streams)
	var wg sync.WaitGroup
	wg.Add(streams)
	for i := range streams {
		go func(i int) {
			defer wg.Done()
			results[i] = timedGet(ctx, payloadURL)
		}(i)
	}
	wg.Wait()
	return results
}

// percentiles reports p50/p95/p99 (and count of failures) over the successful
// stream durations.
type percentiles struct {
	p50, p95, p99 time.Duration
	failures      int
	n             int
}

func summarize(results []streamResult) percentiles {
	durs := make([]time.Duration, 0, len(results))
	failures := 0
	for _, r := range results {
		if r.err != nil {
			failures++
			continue
		}
		durs = append(durs, r.duration)
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	return percentiles{
		p50:      pctile(durs, 0.50),
		p95:      pctile(durs, 0.95),
		p99:      pctile(durs, 0.99),
		failures: failures,
		n:        len(durs),
	}
}

func pctile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(q*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// writeCSV appends one row per (transport, loss) arm to a CSV in outDir, so a
// run's arms can be compared and multiple runs accumulated.
func writeCSV(t *testing.T, outDir, experiment string, rows [][]string) string {
	t.Helper()
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("create out dir: %v", err)
	}
	path := filepath.Join(outDir, experiment+".csv")
	_, statErr := os.Stat(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open csv: %v", err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	if os.IsNotExist(statErr) {
		_ = w.Write([]string{"timestamp", "transport", "loss_pct", "streams", "payload_bytes", "p50_ms", "p95_ms", "p99_ms", "failures", "n"})
	}
	for _, row := range rows {
		_ = w.Write(row)
	}
	w.Flush()
	return path
}

func msRow(ts string, tr transport, lossPct float64, streams, payload int, p percentiles) []string {
	return []string{
		ts, string(tr), strconv.FormatFloat(lossPct, 'g', -1, 64),
		strconv.Itoa(streams), strconv.Itoa(payload),
		strconv.FormatInt(p.p50.Milliseconds(), 10),
		strconv.FormatInt(p.p95.Milliseconds(), 10),
		strconv.FormatInt(p.p99.Milliseconds(), 10),
		strconv.Itoa(p.failures), strconv.Itoa(p.n),
	}
}
