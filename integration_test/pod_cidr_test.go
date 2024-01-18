package integration_test

import (
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

type podCIDRSuite struct {
	itest.Suite
	itest.NamespacePair
}

func (s *podCIDRSuite) SuiteName() string {
	return "PodCIDRStrategy"
}

func init() {
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &podCIDRSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *podCIDRSuite) Test_PodCIDRStrategy() {
	ctx := s.Context()
	connected := false

	rq := s.Require()
	rq.NoError(s.TelepresenceHelmInstall(ctx, false))
	defer func() {
		if connected {
			itest.TelepresenceQuitOk(ctx)
		}
		s.UninstallTrafficManager(ctx, s.ManagerNamespace())
	}()
	s.TelepresenceConnect(ctx)
	connected = true
	si := itest.TelepresenceStatusOk(ctx)
	itest.TelepresenceQuitOk(ctx)
	connected = false

	subnetsAsStrings := func(snn []*iputil.Subnet) []string {
		sns := make([]string, len(snn))
		for i, sn := range snn {
			sns[i] = sn.String()
		}
		return sns
	}
	dlog.Infof(ctx, "subnets %v", si.RootDaemon.Subnets)
	podCIDRs := subnetsAsStrings(si.RootDaemon.Subnets[1:])

	tests := []struct {
		name        string
		values      map[string]string
		wantSubnets []string
	}{
		{
			"environment",
			map[string]string{
				"podCIDRs":        strings.Join(append(podCIDRs, "199.199.50.228/30"), " "),
				"podCIDRStrategy": "environment",
			},
			append(podCIDRs, "199.199.50.228/30"),
		},
	}

	vDir := s.T().TempDir()
	vFile := filepath.Join(vDir, "values.yaml")

	for _, tt := range tests {
		tt := tt
		s.Run(tt.name, func() {
			rq := s.Require()
			vy, err := yaml.Marshal(tt.values)
			rq.NoError(err)
			rq.NoError(os.WriteFile(vFile, vy, 0o644))
			rq.NoError(s.TelepresenceHelmInstall(ctx, true, "--no-hooks", "-f", vFile))
			s.TelepresenceConnect(ctx)
			connected = true
			si := itest.TelepresenceStatusOk(ctx)
			itest.TelepresenceQuitOk(ctx)
			connected = false
			rd := si.RootDaemon
			rq.NotNil(rd)
			rq.Greater(len(rd.Subnets), 1)
			s.Equal(tt.wantSubnets, subnetsAsStrings(rd.Subnets[1:]))
		})
	}
}
