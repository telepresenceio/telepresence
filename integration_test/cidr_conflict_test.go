package integration_test

import (
	"encoding/json/v2"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"

	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/slice"
	"github.com/telepresenceio/telepresence/v2/pkg/vif"
)

type cidrConflictSuite struct {
	itest.Suite
	itest.TrafficManager
	vipSubnet netip.Prefix
	subnets   []netip.Prefix
	scripts   string
}

func (s *cidrConflictSuite) SuiteName() string {
	return "CIDRConflict"
}

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &cidrConflictSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h}
	})
}

func (s *cidrConflictSuite) SetupSuite() {
	if runtime.GOOS != "linux" {
		s.T().Skip("we can only create veth interfaces on linux")
	}
	const svc = "echo"
	s.Suite.SetupSuite()
	tpl := &itest.Generic{
		Name:     svc,
		Registry: "ghcr.io/telepresenceio",
		Image:    "echo-server:latest",
		Environment: []core.EnvVar{
			{
				Name:  "PORTS",
				Value: "8080",
			},
			{
				Name: "LISTEN_ADDRESS",
				ValueFrom: &core.EnvVarSource{
					FieldRef: &core.ObjectFieldSelector{
						FieldPath: "status.podIP",
					},
				},
			},
		},
		Annotations: map[string]string{
			annotation.InjectTrafficAgent: "enabled",
		},
	}
	s.ApplyTemplate(s.Context(), filepath.Join("testdata", "k8s", "generic.goyaml"), &tpl)
	s.NoError(s.RolloutStatusWait(s.Context(), "deploy/echo"))

	ctx := s.Context()
	s.TelepresenceConnect(ctx)
	st := itest.TelepresenceStatusOk(ctx)
	itest.TelepresenceQuit(ctx)
	s.subnets = st.RootDaemon.Subnets
	if len(s.subnets) < 2 {
		s.T().Skip("Test cannot run unless client maps at least two subnets")
	}
	var err error
	s.scripts, err = filepath.Abs(filepath.Join("testdata", "scripts"))
	if s.NoError(err) {
		// Create an interface that will be in conflict with the service and pod subnets.
		s.NoError(itest.Run(ctx, "sudo", filepath.Join(s.scripts, "veth-up.sh"), s.subnets[0].String(), s.subnets[1].String()))
		s.NoError(err)
	}
	s.vipSubnet = client.GetConfig(ctx).Routing().VirtualSubnet
}

func (s *cidrConflictSuite) TearDownSuite() {
	ctx := s.Context()
	s.NoError(itest.Run(ctx, "sudo", filepath.Join(s.scripts, "veth-down.sh"), s.subnets[0].String(), s.subnets[1].String()))
	s.DeleteSvcAndWorkload(ctx, "deploy", "echo")
}

func (s *cidrConflictSuite) Test_AutoConflictResolution() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx)
	st := itest.TelepresenceStatusOk(ctx)
	defer itest.TelepresenceQuit(ctx)
	sns := st.RootDaemon.Subnets
	rq := s.Require()

	// virtualSubnetFor returns the Telepresence-owned virtual subnet that a cluster
	// address of the given family is translated into: the configured (IPv4) virtual
	// subnet, or the fixed IPv6 ULA.
	virtualSubnetFor := func(a netip.Addr) netip.Prefix {
		if a.Is6() {
			return vif.TelepresenceULA6
		}
		return s.vipSubnet
	}
	isVirtual := func(a netip.Addr) bool {
		return s.vipSubnet.Contains(a) || vif.TelepresenceULA6.Contains(a)
	}

	// Each conflicting subnet must have been replaced by the virtual subnet of its
	// own address family. On a dual-stack cluster the two conflicts belong to
	// different families and therefore map to two different virtual subnets, so the
	// subnet count need not shrink; what matters is that the conflicts are gone and
	// the matching virtual subnets are routed.
	for _, c := range []netip.Prefix{s.subnets[0], s.subnets[1]} {
		rq.NotContains(sns, c, "conflicting subnet %s should have been replaced by a virtual subnet", c)
		rq.Contains(sns, virtualSubnetFor(c.Addr()), "virtual subnet for the family of %s should be routed", c)
	}

	// Ingest to get a container environment.
	envFile := filepath.Join(s.T().TempDir(), "echo.env")
	itest.TelepresenceOk(ctx, "ingest", "echo", "--env-file", envFile, "--env-syntax", "json")
	itest.TelepresenceOk(ctx, "detach", "echo")
	var env map[string]string
	envData, err := os.ReadFile(envFile)
	rq.NoError(err)
	err = json.Unmarshal(envData, &env)
	rq.NoError(err)

	// The service address lies in a conflicting subnet, so it must have been
	// translated into a virtual IP. Any environment address that became virtual must
	// be in its own family's virtual subnet (an IPv6 address must not land in the
	// IPv4 virtual subnet, and vice versa).
	for _, key := range []string{"LISTEN_ADDRESS", "ECHO_SERVICE_HOST"} {
		addrVal, ok := env[key]
		rq.True(ok)
		addr, err := netip.ParseAddr(addrVal)
		rq.NoError(err)
		if key == "ECHO_SERVICE_HOST" {
			rq.Truef(isVirtual(addr), "ECHO_SERVICE_HOST %s should have been translated to a virtual IP", addr)
		}
		if isVirtual(addr) {
			rq.Truef(virtualSubnetFor(addr).Contains(addr), "%s %s is not in its own family's virtual subnet %s", key, addr, virtualSubnetFor(addr))
		}
	}
}

func (s *cidrConflictSuite) Test_AutoConflictAvoidance() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx, "--allow-conflicting-subnets", fmt.Sprintf("%s,%s", s.subnets[0], s.subnets[1]))
	st := itest.TelepresenceStatusOk(ctx)
	defer itest.TelepresenceQuitOk(ctx)
	sns := st.RootDaemon.Subnets
	s.Require().Equal(slice.AsStrings(s.subnets), slice.AsStrings(sns), "subnet conflict should not be resolved using VNAT")
}

func (s *cidrConflictSuite) Test_AutoConflictResolution_CloudDisable() {
	ctx := s.Context()
	s.TelepresenceHelmInstallOK(ctx, true, "--set", "client.routing.autoResolveConflicts=false")
	defer s.RollbackTM(ctx)

	_, err := s.TelepresenceTryConnect(ctx)
	s.Require().Error(err)
}

func (s *cidrConflictSuite) Test_AutoConflictResolution_ClientDisable() {
	ctx := itest.WithConfig(s.Context(), func(cfg client.Config) {
		cfg.Routing().AutoResolveConflicts = false
	})
	_, err := s.TelepresenceTryConnect(ctx)
	s.Require().Error(err)
}

func (s *cidrConflictSuite) Test_AllowConflictResolution() {
	ctx := itest.WithConfig(s.Context(), func(cfg client.Config) {
		cfg.Routing().AutoResolveConflicts = false
		cfg.Routing().AllowConflicting = s.subnets
	})

	testIP := net.IP(s.subnets[0].Addr().AsSlice())
	testIP[len(testIP)-1] = 37

	// Verify that a route in the conflicting subnet is routed via brm
	out, err := itest.Output(ctx, "ip", "route", "get", testIP.String())
	rq := s.Require()
	rq.NoError(err)
	rq.Contains(out, "dev brm")

	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceQuitOk(ctx)
	st := itest.TelepresenceStatusOk(ctx)
	defer itest.TelepresenceQuitOk(ctx)
	sns := st.RootDaemon.Subnets
	rq.Equal(sns, s.subnets, "Subnets should not change but %v != %v", sns, s.subnets)

	// Verify that a route in the conflicting subnet is routed via Telepresence
	out, err = itest.Output(ctx, "ip", "route", "get", testIP.String())
	rq.NoError(err)
	rq.Contains(out, "dev tel0") // tel0 is OK, we only run this on linux
}

// Test_LocalDNSStaysReachable verifies that a local DNS server whose address falls
// inside a routed (here: allow-conflicting) subnet is kept reachable on its original
// interface instead of being tunnelled into the cluster. See issue #2429.
func (s *cidrConflictSuite) Test_LocalDNSStaysReachable() {
	rq := s.Require()

	dnsIP := net.IP(s.subnets[0].Addr().AsSlice())
	dnsIP[len(dnsIP)-1] = 53
	dnsAddr, ok := netip.AddrFromSlice(dnsIP)
	rq.True(ok)
	dnsHostRoute := netip.PrefixFrom(dnsAddr, dnsAddr.BitLen())

	ctx := itest.WithConfig(s.Context(), func(cfg client.Config) {
		cfg.Routing().AutoResolveConflicts = false
		cfg.Routing().AllowConflicting = s.subnets
		cfg.DNS().LocalAddresses = []netip.AddrPort{netip.AddrPortFrom(dnsAddr, 53)}
	})

	// Before connecting, the DNS server is reachable via the conflicting veth interface.
	out, err := itest.Output(ctx, "ip", "route", "get", dnsAddr.String())
	rq.NoError(err)
	rq.Contains(out, "dev brm")

	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceQuitOk(ctx)

	// The conflicting DNS server must be added to never-proxy automatically.
	st := itest.TelepresenceStatusOk(ctx)
	rq.Contains(st.RootDaemon.NeverProxy, dnsHostRoute,
		"local DNS server %s should be added to never-proxy", dnsAddr)

	// And it must remain reachable on its original interface, not via tel0.
	out, err = itest.Output(ctx, "ip", "route", "get", dnsAddr.String())
	rq.NoError(err)
	rq.Contains(out, "dev brm")
}
