package rt

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	usg "github.com/telepresenceio/telepresence/rpc/v2/usg"
)

// UsageCollector is an in-process gRPC server implementing the usg
// (rpc/v2/usg) service's Report/ReportBatch RPCs. It is the local stand-in
// for the real usage-reporting collector: point a manager spec's
// usage.collectorAddress at it (managers.UsageTo) and a client config's
// Usage().CollectorAddress at LocalAddr, then assert on Reports.
//
// It listens on all interfaces (not just loopback): the traffic-manager
// runs inside the cluster and reaches the collector via the cluster's view
// of the host (see ProbeUsageCollectorReachable), which requires the
// listening socket to accept connections arriving from outside the host,
// not just 127.0.0.1. A same-host client (the CLI under test) can still
// dial it over loopback via LocalAddr.
type UsageCollector struct {
	usg.UnimplementedUsgServer

	lis    net.Listener
	server *grpc.Server

	mu      sync.Mutex
	reports []*usg.UsageReport
}

// NewUsageCollector starts the collector and returns it; stop it with
// Close. Every Report/ReportBatch call the resulting server receives,
// including ones that race Close, is captured before the listener is torn
// down.
func NewUsageCollector() (*UsageCollector, error) {
	lis, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("rtest: usage collector: listen: %w", err)
	}
	c := &UsageCollector{lis: lis, server: grpc.NewServer()}
	usg.RegisterUsgServer(c.server, c)
	go func() {
		if err := c.server.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			fmt.Fprintf(os.Stderr, "rtest: usage collector: serve: %v\n", err)
		}
	}()
	return c, nil
}

// Addr returns the address the collector listens on: 0.0.0.0:<port>. Build
// a cluster-facing address around c.Port() instead (see
// ProbeUsageCollectorReachable); use LocalAddr for a same-host client.
func (c *UsageCollector) Addr() string {
	return c.lis.Addr().String()
}

// Port returns the port component of Addr.
func (c *UsageCollector) Port() int {
	_, p, _ := net.SplitHostPort(c.Addr())
	n, _ := strconv.Atoi(p)
	return n
}

// LocalAddr returns the host:port a client running on this same host should
// dial: 127.0.0.1:<port>.
func (c *UsageCollector) LocalAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", c.Port())
}

// Report implements usg.UsgServer.
func (c *UsageCollector) Report(_ context.Context, r *usg.UsageReport) (*emptypb.Empty, error) {
	c.mu.Lock()
	c.reports = append(c.reports, r)
	c.mu.Unlock()
	return &emptypb.Empty{}, nil
}

// ReportBatch implements usg.UsgServer.
func (c *UsageCollector) ReportBatch(_ context.Context, b *usg.UsageReportBatch) (*emptypb.Empty, error) {
	c.mu.Lock()
	c.reports = append(c.reports, b.GetReports()...)
	c.mu.Unlock()
	return &emptypb.Empty{}, nil
}

// Reports returns a snapshot of every report received so far (individually,
// or as part of a batch), in receive order.
func (c *UsageCollector) Reports() []*usg.UsageReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*usg.UsageReport{}, c.reports...)
}

// Close stops the collector, waiting for in-flight RPCs to complete.
func (c *UsageCollector) Close() {
	c.server.GracefulStop()
}

// ProbeUsageCollectorReachable determines whether the cluster can reach c
// the way a real workstation would appear to it: via host.docker.internal.
// It mirrors usage_reporting_test.go's skip probe exactly --
// resolveHostFromCluster's nslookup, then a reachability check against c's
// actual port -- so a suite can self-skip on environments (e.g. non-Docker
// Linux container runtimes) where that address doesn't exist, exactly as
// the superseded suite did, rather than fail. Both probe pods run in ns
// (typically managers.ManagerNamespace, matching the old suite).
//
// On success it returns the address the traffic-manager should be pointed
// at (managers.UsageTo) -- host.docker.internal's cluster-resolved IP,
// combined with c's port -- and ok=true. On failure ok is false and reason
// explains why, suitable for t.Skipf(reason).
func ProbeUsageCollectorReachable(e Env, ns string, c *UsageCollector) (addr string, ok bool, reason string) {
	e.T.Helper()
	hostIP, err := resolveHostDockerInternal(e, ns)
	if err != nil {
		return "", false, fmt.Sprintf("cluster cannot resolve host.docker.internal: %v", err)
	}
	port := c.Port()
	if err := probeHostPortReachable(e, ns, hostIP, port); err != nil {
		return "", false, fmt.Sprintf("host not reachable from cluster at %s:%d: %v", hostIP, port, err)
	}
	return fmt.Sprintf("%s:%d", hostIP, port), true, ""
}

// resolveHostDockerInternal runs a short-lived busybox pod in ns that asks
// cluster DNS to resolve host.docker.internal, and returns the first IPv4
// address in the reply. Mirrors usage_reporting_test.go's
// resolveHostFromCluster.
func resolveHostDockerInternal(e Env, ns string) (string, error) {
	out, err := e.R.Kubectl(e.Ctx, ns,
		"run", "usg-host-probe-"+randomHex(6),
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
			// Accept IPv4 only -- busybox prints both v4 and v6 entries.
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

// probeHostPortReachable runs a short-lived busybox pod in ns that attempts
// a TCP connect to hostIP:port, failing (a non-nil error) if it can't
// connect within 2s. Mirrors usage_reporting_test.go's reach probe.
func probeHostPortReachable(e Env, ns, hostIP string, port int) error {
	_, err := e.R.Kubectl(e.Ctx, ns,
		"run", "usg-reach-probe-"+randomHex(6),
		"--image=busybox:1.36",
		"--restart=Never",
		"--rm", "-i",
		"--quiet",
		"--command", "--",
		"sh", "-c", fmt.Sprintf("nc -z -w 2 %s %d", hostIP, port))
	return err
}
