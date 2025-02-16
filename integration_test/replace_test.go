package integration_test

import (
	"fmt"
	"path/filepath"
	"time"

	core "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
)

type replaceSuite struct {
	itest.Suite
	itest.TrafficManager
	svc     string
	tplPath string
	tpl     *itest.Generic
}

func (s *replaceSuite) SuiteName() string {
	return "Replace"
}

func init() {
	itest.AddConnectedSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &replaceSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h, svc: "echo-server"}
	})
}

func (s *replaceSuite) SetupSuite() {
	if !(s.ManagerIsVersion(">2.21.x") && s.ClientIsVersion(">2.21.x")) {
		s.T().Skip("Not part of compatibility tests. The replace command was introduced in 2.22")
	}
	s.Suite.SetupSuite()
	s.tplPath = filepath.Join("testdata", "k8s", "generic.goyaml")
	s.tpl = &itest.Generic{
		Name:       s.svc,
		TargetPort: "http",
		Registry:   "ghcr.io/telepresenceio",
		Image:      "echo-server:latest",
		ServicePorts: []itest.ServicePort{
			{
				Number:     80,
				Name:       "http",
				TargetPort: "http-cp",
			},
			{
				Number:     81,
				Name:       "extra",
				TargetPort: "extra-cp",
			},
		},
		ContainerPorts: []itest.ContainerPort{
			{
				Number: 8080,
				Name:   "http-cp",
			},
			{
				Number: 8081,
				Name:   "extra-cp",
			},
		},
		Environment: []core.EnvVar{
			{
				Name:  "PORTS",
				Value: "8080,8081",
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
			agentconfig.InjectAnnotation: "enabled",
		},
	}
	ctx := s.Context()
	s.ApplyTemplate(ctx, s.tplPath, s.tpl)
	s.NoError(s.RolloutStatusWait(ctx, "deploy/"+s.svc))
}

func (s *replaceSuite) TearDownSuite() {
	s.DeleteTemplate(s.Context(), s.tplPath, s.tpl)
}

func (s *replaceSuite) Test_ReplaceWithMultiContainerPorts() {
	ctx := s.Context()
	_, httpCancel := itest.StartLocalHttpEchoServerWithAddr(ctx, s.svc+"-http", "localhost:8080")
	defer httpCancel()
	_, extraCancel := itest.StartLocalHttpEchoServerWithAddr(ctx, s.svc+"-extra", "localhost:8081")
	defer extraCancel()

	// Use container port names here.
	so := itest.TelepresenceOk(ctx, "replace", s.svc)
	mustLeave := true
	defer func() {
		if mustLeave {
			itest.TelepresenceOk(ctx, "leave", s.svc)
		}
	}()

	rq := s.Require()
	rq.Contains(so, "Using Deployment "+s.svc)

	itest.PingInterceptedEchoServer(ctx, fmt.Sprintf("%s/%s-http", s.svc, s.svc), "80")
	itest.PingInterceptedEchoServer(ctx, fmt.Sprintf("%s/%s-extra", s.svc, s.svc), "81")

	itest.TelepresenceOk(ctx, "leave", s.svc)
	mustLeave = false

	// Ensure that we now reach the original app again.
	s.Eventually(func() bool {
		out, err := itest.Output(ctx, "curl", "--verbose", "--max-time", "1", s.svc)
		dlog.Infof(ctx, "Received %s", out)
		if err != nil {
			dlog.Errorf(ctx, "curl error %v", err)
			return false
		}
		return true
	}, 30*time.Second, 2*time.Second, "Pod app is not reachable after ending the replace")
}
