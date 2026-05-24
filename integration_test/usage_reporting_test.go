package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/clog"
	usgrpc "github.com/telepresenceio/telepresence/rpc/v2/usg"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

// fakeUsgCollector is a minimal in-process gRPC server that implements the
// telepresence.usg.Usg service. Every received UsageReport (including those
// arriving in a ReportBatch) is persisted to a separate file in the sink
// directory. Filenames are timestamp-prefixed so directory ordering matches
// receive order.
type fakeUsgCollector struct {
	usgrpc.UnimplementedUsgServer

	addr   string
	dir    string
	server *grpc.Server
	lis    net.Listener
	seq    atomic.Uint64

	mu sync.Mutex
}

// newFakeUsgCollector starts a gRPC collector listening on 0.0.0.0:0 (any free
// host port) and writing every received report to dir. The directory is
// created if it does not exist. Stop the collector with Close.
func newFakeUsgCollector(_ context.Context, dir string) (*fakeUsgCollector, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	lis, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, err
	}
	c := &fakeUsgCollector{
		dir:    dir,
		lis:    lis,
		server: grpc.NewServer(),
		addr:   lis.Addr().String(),
	}
	usgrpc.RegisterUsgServer(c.server, c)
	go func() {
		if err := c.server.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			fmt.Fprintf(os.Stderr, "fakeUsgCollector serve error: %v\n", err)
		}
	}()
	return c, nil
}

// Port returns the port the collector is listening on; callers construct a
// host expression (host.docker.internal:port, 127.0.0.1:port, ...) around it.
func (c *fakeUsgCollector) Port() int {
	_, p, _ := net.SplitHostPort(c.addr)
	var n int
	_, _ = fmt.Sscanf(p, "%d", &n)
	return n
}

func (c *fakeUsgCollector) Close() {
	c.server.GracefulStop()
	_ = c.lis.Close()
}

func (c *fakeUsgCollector) Report(_ context.Context, r *usgrpc.UsageReport) (*emptypb.Empty, error) {
	c.persist(r)
	return &emptypb.Empty{}, nil
}

func (c *fakeUsgCollector) ReportBatch(_ context.Context, b *usgrpc.UsageReportBatch) (*emptypb.Empty, error) {
	for _, r := range b.GetReports() {
		c.persist(r)
	}
	return &emptypb.Empty{}, nil
}

func (c *fakeUsgCollector) persist(r *usgrpc.UsageReport) {
	if r == nil {
		return
	}
	data, err := proto.Marshal(r)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	seq := c.seq.Add(1)
	name := fmt.Sprintf("%s-%06d.pb", time.Now().UTC().Format("20060102T150405.000000000"), seq)
	tmp := filepath.Join(c.dir, name+".tmp")
	final := filepath.Join(c.dir, name)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, final)
}

// Reports reads every persisted report from disk, in insertion order.
func (c *fakeUsgCollector) Reports() ([]*usgrpc.UsageReport, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".pb" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := make([]*usgrpc.UsageReport, 0, len(names))
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(c.dir, n))
		if err != nil {
			continue
		}
		r := new(usgrpc.UsageReport)
		if err := proto.Unmarshal(data, r); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

type usageReportingSuite struct {
	itest.Suite
	itest.NamespacePair

	collector *fakeUsgCollector
}

func (s *usageReportingSuite) SuiteName() string {
	return "UsageReporting"
}

func init() {
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &usageReportingSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

// resolveHostFromCluster runs a one-shot pod that asks cluster DNS to resolve
// host.docker.internal. The returned IP is the host's address from a pod's
// perspective on Docker Desktop and on modern kind clusters. An empty string
// signals "not resolvable" and the caller should skip.
func resolveHostFromCluster(ctx context.Context, ns string) (string, error) {
	out, err := itest.KubectlOut(ctx, ns,
		"run", "usg-host-probe",
		"--image=busybox:1.36",
		"--restart=Never",
		"--rm", "-i",
		"--quiet",
		"--command", "--",
		"nslookup", "host.docker.internal")
	if err != nil {
		return "", err
	}
	// Skip the first "Address:" line (the DNS server), keep the first IPv4
	// "Address:" line that follows a "Name:" line.
	var ip string
	var afterName bool
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Name:") {
			afterName = true
			continue
		}
		if afterName && strings.HasPrefix(line, "Address:") {
			candidate := strings.TrimSpace(strings.TrimPrefix(line, "Address:"))
			// Accept IPv4 only — busybox prints both v4 and v6 entries.
			if strings.Count(candidate, ".") == 3 {
				ip = candidate
				break
			}
		}
	}
	if ip == "" {
		return "", fmt.Errorf("host.docker.internal did not resolve to an IPv4 address; nslookup output:\n%s", out)
	}
	return ip, nil
}

func (s *usageReportingSuite) SetupSuite() {
	s.Suite.SetupSuite()
	ctx := s.Context()
	rq := s.Require()

	hostIP, err := resolveHostFromCluster(ctx, s.ManagerNamespace())
	if err != nil {
		s.T().Skipf("cluster cannot resolve host.docker.internal: %v", err)
		return
	}
	clog.Infof(ctx, "usg collector: cluster resolves host at %s", hostIP)

	collector, err := newFakeUsgCollector(ctx, s.T().TempDir())
	rq.NoError(err)
	s.collector = collector

	// Verify the host is actually reachable from inside the cluster at the
	// chosen address. We do this with one more probe pod so a confused
	// environment fails the setup cleanly rather than producing a flaky
	// "no reports arrived" failure later.
	mgrAddr := fmt.Sprintf("%s:%d", hostIP, collector.Port())
	if err := itest.Kubectl(ctx, s.ManagerNamespace(),
		"run", "usg-reach-probe",
		"--image=busybox:1.36",
		"--restart=Never",
		"--rm", "-i",
		"--quiet",
		"--command", "--",
		"sh", "-c", fmt.Sprintf("nc -z -w 2 %s %d", hostIP, collector.Port())); err != nil {
		collector.Close()
		s.T().Skipf("host not reachable from cluster at %s: %v", mgrAddr, err)
		return
	}

	// Install the traffic-manager with usage reporting enabled and pointed at
	// the fake collector. The chart default (overridden by the itest harness
	// to "disabled") is restored to "enabled" via --set for this suite.
	s.TelepresenceHelmInstallOK(ctx, false,
		"--set", "usage.enabled=true",
		"--set", "usage.collectorAddress="+mgrAddr,
		"--set", "usage.insecure=true",
	)
	s.ApplyEchoService(ctx, "echo-usg", 80)
}

func (s *usageReportingSuite) TearDownSuite() {
	ctx := s.Context()
	if s.collector != nil {
		s.UninstallTrafficManager(ctx, s.ManagerNamespace())
		s.collector.Close()
	}
}

func (s *usageReportingSuite) Test_UsageReportedFromClientAndManager() {
	rq := s.Require()
	// Enable usage on the client side and point it at the fake collector via
	// localhost. WithConfig quits any running daemon so the next connect picks
	// up the new config and starts the sender goroutine.
	collectorHostPort := fmt.Sprintf("127.0.0.1:%d", s.collector.Port())
	ctx := itest.WithConfig(s.Context(), func(cfg client.Config) {
		u := cfg.Usage()
		u.Enabled = true
		u.CollectorAddress = collectorHostPort
		u.Insecure = true
	})

	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceQuitOk(ctx)

	stdout := itest.TelepresenceOk(ctx, "intercept", "--mount", "false", "echo-usg", "--port", "9090")
	s.Contains(stdout, "Using Deployment echo-usg")
	defer itest.TelepresenceOk(ctx, "leave", "echo-usg")

	// The sender flushes every 30s; allow up to 90s before declaring failure.
	// We need at least one client report (e.g. cmd.connect or cmd.intercept)
	// and at least one manager report (e.g. manager.boot).
	deadline := time.Now().Add(90 * time.Second)
	var lastClient, lastManager int
	for time.Now().Before(deadline) {
		reports, err := s.collector.Reports()
		rq.NoError(err)
		var clientCount, managerCount int
		for _, r := range reports {
			switch r.GetSource() {
			case "client":
				clientCount++
			case "manager":
				managerCount++
			}
		}
		if clientCount > 0 && managerCount > 0 {
			s.assertReportShape(reports)
			return
		}
		lastClient, lastManager = clientCount, managerCount
		time.Sleep(3 * time.Second)
	}
	s.T().Fatalf("did not receive usage reports from both sides within 90s: client=%d manager=%d",
		lastClient, lastManager)
}

// assertReportShape checks invariants that hold for every UsageReport the
// collector receives, regardless of source.
func (s *usageReportingSuite) assertReportShape(reports []*usgrpc.UsageReport) {
	t := s.T()
	for _, r := range reports {
		if r.GetInstallationId() == "" {
			t.Errorf("report missing installation id: %+v", r)
		}
		if r.GetTopic() == "" {
			t.Errorf("report missing topic: %+v", r)
		}
		if r.GetSource() != "client" && r.GetSource() != "manager" {
			t.Errorf("unexpected source %q in report: %+v", r.GetSource(), r)
		}
	}
}
