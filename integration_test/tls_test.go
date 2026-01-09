package integration_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
)

func (s *dockerDaemonSuite) Test_TLSAnnotations() {
	if !(s.ManagerIsVersion(">2.24.x") && s.ClientIsVersion(">2.24.x")) {
		s.T().Skip("Not part of compatibility tests. Versions < 2.25.0 have no support for http intercepts")
	}
	const (
		svc           = "hello"
		containerPort = 8443
		image         = "echo-server:latest"
		registry      = "ghcr.io/telepresenceio"
	)

	tlsAppTemplate := itest.Generic{
		Name: svc,
		ServicePorts: []itest.ServicePort{
			{
				Number:     443,
				Name:       "https",
				TargetPort: "https",
			},
		},
		ContainerPorts: []itest.ContainerPort{
			{
				Number: containerPort,
				Name:   "https",
			},
		},
		Environment: []core.EnvVar{
			{
				Name:  "PORTS",
				Value: fmt.Sprintf("%d:https", containerPort),
			},
		},
		Image:    image,
		Registry: registry,
		Volumes: []core.Volume{
			{
				Name: "tls",
				VolumeSource: core.VolumeSource{
					Secret: &core.SecretVolumeSource{SecretName: "tel-cert"},
				},
			},
		},
		VolumeMounts: []core.VolumeMount{
			{
				Name:      "tls",
				ReadOnly:  true,
				MountPath: "/certs",
			},
		},
	}

	ctx := docker.EnableClient(s.Context())
	rq := s.Require()
	dockerCli, err := docker.GetClient(ctx)
	rq.NoError(err)
	k8s := filepath.Join(itest.GetOSSRoot(ctx), "testdata", "k8s")
	genericPath := filepath.Join(k8s, "generic.goyaml")
	rq.NoError(itest.Kubectl(ctx, s.AppNamespace(), "apply", "-f", filepath.Join(k8s, "tel-cert.yaml")))

	s.TelepresenceConnect(ctx, "--docker")
	defer itest.TelepresenceDisconnectOk(ctx)

	type testData struct {
		testName     string
		plainText    bool
		header       string
		anns         map[string]string
		errorPattern string
	}

	tts := []testData{
		{
			"Client TLS and cert secret",
			false,
			"x:y",
			map[string]string{
				annotation.DownstreamTLSSecret + fmt.Sprintf(".%d", containerPort):        "tel-cert",
				annotation.UpstreamInsecureSkipVerify + fmt.Sprintf(".%d", containerPort): "enabled",
				annotation.InjectTrafficAgent:                                             "enabled",
			},
			"",
		},
		{
			"Client plaintext and cert secret",
			true,
			"x:y",
			map[string]string{
				annotation.DownstreamTLSSecret + fmt.Sprintf(".%d", containerPort):        "tel-cert",
				annotation.UpstreamInsecureSkipVerify + fmt.Sprintf(".%d", containerPort): "enabled",
				annotation.InjectTrafficAgent:                                             "enabled",
			},
			"",
		},
		{
			"Client TLS and cert path",
			false,
			"x:y",
			map[string]string{
				annotation.DownstreamCertificatePath + fmt.Sprintf(".%d", containerPort):  "/certs",
				annotation.UpstreamInsecureSkipVerify + fmt.Sprintf(".%d", containerPort): "enabled",
				annotation.InjectTrafficAgent:                                             "enabled",
			},
			"",
		},
		{
			"Client TLS and cert path, not insecure",
			false,
			"x:y",
			map[string]string{
				annotation.DownstreamCertificatePath + fmt.Sprintf(".%d", containerPort): "/certs",
				annotation.InjectTrafficAgent:                                            "enabled",
			},
			"failed to verify certificate",
		},
		{
			"Client mTLS and cert path",
			false,
			"x:y",
			map[string]string{
				annotation.DownstreamCertificatePath + fmt.Sprintf(".%d", containerPort):  "/certs",
				annotation.UpstreamCertificatePath + fmt.Sprintf(".%d", containerPort):    "/certs",
				annotation.UpstreamInsecureSkipVerify + fmt.Sprintf(".%d", containerPort): "enabled",
				annotation.InjectTrafficAgent:                                             "enabled",
			},
			"",
		},
	}

	for i, tt := range tts {
		s.Run(tt.testName, func() {
			ctx = s.Context()
			ttSvc := fmt.Sprintf("%s-%d", svc, i)
			tpl := tlsAppTemplate
			tpl.Name = ttSvc
			tpl.Annotations = tt.anns
			s.ApplyTemplate(ctx, genericPath, &tpl)
			defer s.DeleteTemplate(ctx, genericPath, &tpl)

			rq := s.Require()
			ctx, cancel := context.WithCancel(ctx)
			wg := sync.WaitGroup{}
			wg.Add(1)
			defer wg.Wait()
			defer cancel()

			go func() {
				defer wg.Done()
				defer cancel()
				args := make([]string, 0, 15)
				args = append(args, "intercept", ttSvc)
				if tt.plainText {
					args = append(args, "--plaintext", "-p", "8080:https")
				} else {
					args = append(args, "-p", "8443:https")
				}
				if tt.header != "" {
					args = append(args, "--http-header", tt.header)
				}
				args = append(args,
					"--mount=false",
					"--docker-run",
					"--", "--name", ttSvc+".local")
				if tt.plainText {
					args = append(args, "-e", "PORTS=8080:http")
				}
				args = append(args, registry+"/"+image)
				stdout, _, err := itest.Telepresence(ctx, args...)
				if err != nil {
					clog.Errorf(ctx, "stdout: %s", stdout)
					clog.Error(ctx, err)
				} else {
					clog.Infof(ctx, "stdout: %s", stdout)
				}
			}()

			rq.EventuallyContext(ctx, func() bool {
				ir, err := dockerCli.ContainerInspect(ctx, ttSvc+".local")
				return err == nil && ir.State.Running
			}, 30*time.Second, 3*time.Second, "expected intercepted status never arrived")
			s.CapturePodLogs(ctx, ttSvc, "traffic-agent", s.AppNamespace())

			si := itest.TelepresenceStatusOk(ctx)
			rq.True(len(si.UserDaemon.Intercepts) == 1)
			rq.Equal(si.UserDaemon.Intercepts[0].Name, ttSvc)

			args := []string{"curl", "--max-time", "2", "-s", "-w", "\nStatus: %{http_code}\n", "-k"}
			if tt.header != "" {
				args = append(args, "-H", tt.header)
			}
			args = append(args, fmt.Sprintf("https://%s", ttSvc))
			so, se, err := itest.Telepresence(ctx, args...)
			s.NoError(err)
			if se != "" {
				clog.Error(ctx, se)
			}
			if tt.errorPattern != "" {
				s.Regexp(tt.errorPattern, so)
			} else {
				s.Contains(so, "HTTP/2.0 GET /")
			}
			clog.Info(ctx, so)

			// Terminate the ongoing intercept
			so, se, err = itest.Telepresence(ctx, "leave", ttSvc)
			if so != "" {
				clog.Info(ctx, so)
			}
			if se != "" {
				clog.Info(ctx, se)
			}
			if err != nil {
				clog.Error(ctx, err)
			}
			cancel()
		})
	}
}
