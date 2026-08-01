package docker

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/netip"
	"os/exec"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/compose"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

const (
	// composeLifecycleVolumeName is the docker-visible name of the named
	// volume Test_NamedVolumeSurvivesDown creates (compose.Volume.Name
	// overrides the generated <project>_<key> name).
	composeLifecycleVolumeName = "rtest-compose-named-volume"

	composeVolumePollTimeout  = 30 * time.Second
	composeVolumePollInterval = 2 * time.Second

	// composeLifecycleProjectName names the three-connection project
	// Test_DefaultNetworkNoSubnetConflict brings up; its default network is
	// named <project>_default.
	composeLifecycleProjectName = "rtest-compose-subnet"
)

// ComposeLifecycle proves two `telepresence compose` teardown/network
// properties orthogonal to the per-verb Compose suite: a named volume
// survives a plain `compose down` and is removed only by `down -v`
// (compose_test.go's Test_ComposeDownPreservesNamedVolumes), and a
// three-connection project's default network never lands on a subnet that
// overlaps the cluster's own (compose_test.go's
// Test_ComposeDefaultNetworkNoSubnetConflict). Labeled Slow: each test
// brings a compose project up twice, or juggles three connections.
type ComposeLifecycle struct {
	rt.Suite
}

func init() {
	rt.Register(&ComposeLifecycle{},
		rt.InArea("docker"),
		rt.NeedsManager(managers.Default),
		rt.Requires(rt.Docker),
		rt.On("linux"),
		rt.WithLabels(rt.Slow),
	)
}

// namedVolumeExists reports whether `docker volume inspect
// composeLifecycleVolumeName` succeeds.
func namedVolumeExists(ctx context.Context) bool {
	_, err := dockerOutput(ctx, "volume", "inspect", composeLifecycleVolumeName)
	return err == nil
}

// dockerNetworkSubnets decodes `docker network inspect --format
// {{json .IPAM.Config}} <network>` into its configured subnets.
func dockerNetworkSubnets(ctx context.Context, name string) ([]netip.Prefix, error) {
	raw, err := dockerOutput(ctx, "network", "inspect", "--format", "{{json .IPAM.Config}}", name)
	if err != nil {
		return nil, err
	}
	var cfgs []struct {
		Subnet string `json:"Subnet"`
	}
	if err := json.Unmarshal([]byte(raw), &cfgs); err != nil {
		return nil, fmt.Errorf("decoding %s's IPAM config: %w", name, err)
	}
	subnets := make([]netip.Prefix, 0, len(cfgs))
	for _, c := range cfgs {
		if c.Subnet == "" {
			continue
		}
		p, err := netip.ParsePrefix(c.Subnet)
		if err != nil {
			continue
		}
		subnets = append(subnets, p)
	}
	return subnets, nil
}

// composeLifecycleStatus mirrors the subset of `telepresence status
// --format json`'s daemon object Test_DefaultNetworkNoSubnetConflict
// asserts on. Every compose connection is a docker-mode daemon (compose
// attaches the teleroute network, which only a containerized daemon
// provides), so a single `--use conn-1`-selected connection reports as a
// top-level "daemon" object (pkg/client/cli/cmd/status.go's
// ContainerizedDaemonStatus, embedding *client.RoutingSnake directly),
// never the separate root_daemon/user_daemon pair a host-mode connection
// would produce (see StatusInfo.toMap's InDocker branch). Live-testing
// confirmed this: `status --format json --use conn-1` against a compose
// connection has no root_daemon key at all, so unmarshaling into one (as
// this originally did) silently left Subnets/AlsoProxy at their zero
// value. regression_test/framework/cli.Status only mirrors the top-level
// running/version fields, not these (mirrors suites/routing/helpers.go's
// identically-shaped daemonStatus, widened with AlsoProxy).
type composeLifecycleStatus struct {
	Daemon struct {
		Subnets   []netip.Prefix `json:"subnets"`
		AlsoProxy []netip.Prefix `json:"also_proxy_subnets"`
	} `json:"daemon"`
}

// Test_NamedVolumeSurvivesDown proves a compose service's named volume
// survives `compose down` without -v, and is only removed by `down -v`:
// compose_test.go's Test_ComposeDownPreservesNamedVolumes.
func (s *ComposeLifecycle) Test_NamedVolumeSurvivesDown() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("compose-lifecycle-vol"))

	proj := &compose.Project{
		Connections: []compose.Connection{{Namespace: ns, ManagerNamespace: managers.ManagerNamespace}},
		Services: map[string]*compose.Service{
			wl.Name: {
				XTele: compose.Replace{
					Ports: []string{fmt.Sprintf("%d:%d", composeLocalHTTPPort, composeReplaceContainerPort)},
				},
				Image:   composeEchoImage,
				Command: composeEchoArgs(),
				Volumes: []string{"bundle:/data"},
			},
		},
		Volumes: map[string]*compose.Volume{"bundle": {Name: composeLifecycleVolumeName}},
	}
	path, err := proj.Write(rt.Env{Ctx: ctx, T: t, R: s.R()})
	s.Require().NoError(err, "writing compose project")

	// Cleanups run LIFO: the volume-rm safety net (registered first, so it
	// runs last) only matters if `compose down -v` (registered second, so
	// it runs first) itself failed to remove the volume.
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", "-f", composeLifecycleVolumeName).Run() })
	t.Cleanup(func() { _, _, _ = s.CLI().Run(ctx, "compose", "-f", path, "down", "-v") })
	_ = exec.Command("docker", "volume", "rm", "-f", composeLifecycleVolumeName).Run()

	up := func() {
		_, stderr, err := s.CLI().Run(ctx, "compose", "-f", path, "up", "-d")
		s.Require().NoError(err, "compose up: %s", stderr)
	}
	down := func(extra ...string) {
		args := append([]string{"compose", "-f", path, "down"}, extra...)
		_, stderr, err := s.CLI().Run(ctx, args...)
		s.Require().NoError(err, "compose down: %s", stderr)
	}

	up()
	s.Eventually(func() bool { return namedVolumeExists(ctx) },
		composeVolumePollTimeout, composeVolumePollInterval,
		"named volume %s should be created by compose up", composeLifecycleVolumeName)

	down()
	s.True(namedVolumeExists(ctx),
		"named volume %s must survive 'compose down' without -v", composeLifecycleVolumeName)

	up()
	s.Eventually(func() bool { return namedVolumeExists(ctx) },
		composeVolumePollTimeout, composeVolumePollInterval,
		"named volume %s should be recreated", composeLifecycleVolumeName)

	down("-v")
	s.Eventually(func() bool { return !namedVolumeExists(ctx) },
		composeVolumePollTimeout, composeVolumePollInterval,
		"named volume %s must be removed by 'compose down -v'", composeLifecycleVolumeName)
}

// Test_DefaultNetworkNoSubnetConflict proves a three-connection project's
// default network never lands on a subnet overlapping the cluster's own:
// compose_test.go's Test_ComposeDefaultNetworkNoSubnetConflict. Each named
// connection runs its own containerized daemon with its own teleroute
// network, pushing Docker's sequential IPAM allocation toward the cluster's
// service CIDR; this asserts the fix that steers the compose project's
// default network away from it.
func (s *ComposeLifecycle) Test_DefaultNetworkNoSubnetConflict() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	mgrNS := managers.ManagerNamespace
	wl := s.Workload(workloads.Echo("compose-lifecycle-subnet"))

	proj := &compose.Project{
		Connections: []compose.Connection{
			{Name: "conn-1", Namespace: ns, ManagerNamespace: mgrNS},
			{Name: "conn-2", Namespace: ns, ManagerNamespace: mgrNS},
			{Name: "conn-3", Namespace: ns, ManagerNamespace: mgrNS},
		},
		Services: map[string]*compose.Service{
			"tester-1": {XTele: compose.Connect{Connection: "conn-1"}, Image: composeToolImage, Command: composeSleepInfinityCmd()},
			"tester-2": {XTele: compose.Connect{Connection: "conn-2"}, Image: composeToolImage, Command: composeSleepInfinityCmd()},
			"tester-3": {XTele: compose.Connect{Connection: "conn-3"}, Image: composeToolImage, Command: composeSleepInfinityCmd()},
		},
	}
	path, err := proj.Write(rt.Env{Ctx: ctx, T: t, R: s.R()})
	s.Require().NoError(err, "writing compose project")

	t.Cleanup(func() {
		_, _, _ = s.CLI().Run(ctx, "compose", "-f", path, "--project-name", composeLifecycleProjectName, "down")
	})
	_, stderr, err := s.CLI().Run(ctx, "compose", "-f", path, "--project-name", composeLifecycleProjectName, "up", "-d")
	s.Require().NoError(err, "compose up: %s", stderr)

	var status composeLifecycleStatus
	s.Require().NoError(s.CLI().JSON(ctx, &status, "status", "--format", "json", "--use", "conn-1"))
	clusterCIDRs := append(append([]netip.Prefix{}, status.Daemon.Subnets...), status.Daemon.AlsoProxy...)
	s.Require().NotEmpty(clusterCIDRs, "expected at least one cluster subnet in status")

	defaultNetwork := composeLifecycleProjectName + "_default"
	subnets, err := dockerNetworkSubnets(ctx, defaultNetwork)
	s.Require().NoError(err, "inspecting compose default network %s", defaultNetwork)
	s.Require().NotEmpty(subnets, "expected at least one IPAM subnet on default network %s", defaultNetwork)

	for _, subnet := range subnets {
		for _, cidr := range clusterCIDRs {
			s.False(subnet.Overlaps(cidr),
				"compose default network %s subnet %s overlaps cluster CIDR %s", defaultNetwork, subnet, cidr)
		}
	}

	// wl.ServiceURL() rather than a bare "http://"+wl.SvcName: workloads.
	// Echo's Service exposes only port 8080, so a bare URL (implicit port
	// 80) has no matching kube-proxy DNAT rule and blackholes; see
	// compose.go's Test_Connect doc comment for the live-tested detail.
	url := wl.ServiceURL()
	s.Eventually(func() bool {
		stdout, stderr, err := s.CLI().Run(ctx, "compose", "-f", path, "--project-name", composeLifecycleProjectName,
			"exec", "tester-1", "wget", "-qO-", url)
		if err == nil && len(stdout) > 0 {
			return true
		}
		t.Logf("wget: %v\nstderr:\n%s", err, stderr)
		return false
	}, composeConnectTimeout, composeConnectInterval, "wget of %s from connect container should succeed", url)
}
