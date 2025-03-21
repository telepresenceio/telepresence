package integration_test

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"

	"github.com/go-json-experiment/json"
	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
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
	itest.TelepresenceQuitOk(ctx)
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
	defer itest.TelepresenceQuitOk(ctx)
	sns := st.RootDaemon.Subnets
	rq := s.Require()
	rq.Less(len(sns), len(s.subnets), "pod and service subnets should be combined into one virtual subnet")

	// The first subnet must now be virtual.
	viSn := sns[0]
	rq.Equalf(s.vipSubnet, viSn, "expected %s to be a virtual CIDR", viSn)

	// Ingest to get a container environment.
	envFile := filepath.Join(s.T().TempDir(), "echo.env")
	itest.TelepresenceOk(ctx, "ingest", "echo", "--env-file", envFile, "--env-syntax", "json")
	itest.TelepresenceOk(ctx, "leave", "echo")
	var env map[string]string
	envData, err := os.ReadFile(envFile)
	rq.NoError(err)
	err = json.Unmarshal(envData, &env)
	rq.NoError(err)

	// Verify that these IPs in the environment have been translated into virtual IPs.
	for _, key := range []string{"LISTEN_ADDRESS", "ECHO_SERVICE_HOST"} {
		addrVal, ok := env[key]
		rq.True(ok)
		addr, err := netip.ParseAddr(addrVal)
		rq.NoError(err)
		rq.Truef(viSn.Contains(addr), "virtual subnet %s does not contain %s %s", viSn, key, addr)
	}
}

func (s *cidrConflictSuite) Test_AutoConflictAvoidance() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx, "--allow-conflicting-subnets", fmt.Sprintf("%s,%s", s.subnets[0], s.subnets[1]))
	st := itest.TelepresenceStatusOk(ctx)
	defer itest.TelepresenceQuitOk(ctx)
	sns := st.RootDaemon.Subnets
	s.Require().Equal(s.subnets, sns, "subnet conflict should not be resolved using VNAT")
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
