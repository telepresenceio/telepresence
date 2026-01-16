package integration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type mountsSuite struct {
	itest.Suite
	itest.TrafficManager
}

func (s *mountsSuite) SuiteName() string {
	return "Mounts"
}

func init() {
	itest.AddConnectedSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &mountsSuite{
			Suite:          itest.Suite{Harness: h},
			TrafficManager: h,
		}
	})
}

func (s *mountsSuite) SetupSuite() {
	if s.IsCI() && runtime.GOOS == "darwin" {
		s.T().Skip("Mount tests don't run on darwin due to macFUSE issues")
		return
	}
	s.Suite.SetupSuite()
}

func (s *mountsSuite) createDeployment() [3]itest.TplResource {
	ctx := s.Context()
	rq := s.Require()

	pv := &itest.PersistentVolume{
		Name: "local-pv",
	}
	if s.UseLocalPathProvisioner() {
		pv.Annotations = map[string]string{
			"pv.kubernetes.io/provisioned-by": "rancher.io/local-path",
		}
		pv.StorageClassName = "local-path"
	}
	rq.NoError(pv.Apply(ctx, s.AppNamespace()))

	pvc := &itest.PersistentVolumeClaim{
		Name: "local-pvc",
	}
	if s.UseLocalPathProvisioner() {
		pvc.Annotations = map[string]string{
			"pv.kubernetes.io/provisioned-by": "rancher.io/local-path",
		}
		pvc.StorageClassName = "local-path"
	}
	rq.NoError(pvc.Apply(ctx, s.AppNamespace()))

	dep := &itest.Generic{
		Name:     "hello",
		Registry: "ghcr.io/telepresenceio",
		Image:    "echo-server:latest",
		Volumes: []core.Volume{
			{
				Name: "rw-volume",
				VolumeSource: core.VolumeSource{
					PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: pvc.Name},
				},
			},
		},
		VolumeMounts: []core.VolumeMount{
			{
				Name:      "rw-volume",
				MountPath: "/data",
			},
		},
	}
	rq.NoError(dep.Apply(ctx, s.AppNamespace()))
	rq.NoError(s.RolloutStatusWait(ctx, "deployment/hello"))
	return [3]itest.TplResource{pv, pvc, dep}
}

func (s *mountsSuite) deleteDeployment(ts [3]itest.TplResource) {
	// Delete in reverse order
	for i := 2; i >= 0; i-- {
		s.Require().NoError(ts[i].Delete(s.Context()))
	}
}

func (s *mountsSuite) Test_MountWrite() {
	if runtime.GOOS == "windows" {
		s.T().SkipNow()
	}
	ts := s.createDeployment()
	defer s.deleteDeployment(ts)

	ctx := s.Context()
	mountPoint := filepath.Join(s.T().TempDir(), "mnt")
	itest.TelepresenceOk(ctx, "intercept", "hello", "--mount", mountPoint, "--port", "80:80")
	time.Sleep(2 * time.Second)

	content := "hello world\n"
	path := filepath.Join(mountPoint, "data", "hello.txt")
	rq := s.Require()
	rq.NoError(os.WriteFile(path, []byte(content), 0o644))
	itest.TelepresenceOk(ctx, "leave", "hello")
	time.Sleep(2 * time.Second)

	mountPoint = filepath.Join(s.T().TempDir(), "data")
	itest.TelepresenceOk(ctx, "intercept", "hello", "--mount", mountPoint, "--port", "80:80")
	defer itest.TelepresenceOk(ctx, "leave", "hello")
	s.CapturePodLogs(ctx, "hello", "traffic-agent", s.AppNamespace())

	path = filepath.Join(mountPoint, "data", "hello.txt")
	data, err := os.ReadFile(path)
	rq.NoError(err)
	rq.Equal(content, string(data))
}

func (s *mountsSuite) Test_MountReadOnly() {
	if runtime.GOOS == "windows" {
		s.T().SkipNow()
	}
	rs := s.createDeployment()
	defer s.deleteDeployment(rs)
	ctx := s.Context()

	mountPoint := filepath.Join(s.T().TempDir(), "mnt")
	itest.TelepresenceOk(ctx, "intercept", "hello", "--mount", mountPoint+":ro", "--port", "80:80")
	defer itest.TelepresenceOk(ctx, "leave", "hello")
	time.Sleep(2 * time.Second)
	s.Require().Error(os.WriteFile(filepath.Join(mountPoint, "data", "hello.txt"), []byte("hello world\n"), 0o644))
}

// Test_CollidingMounts tests that multiple mounts from several containers are managed correctly
// by the traffic-agent and that an intercept of a container mounts the expected volumes.
func (s *mountsSuite) Test_CollidingMounts() {
	ctx := s.Context()
	s.ApplyTemplate(ctx, filepath.Join("testdata", "k8s", "hello-w-volumes.goyaml"), nil)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", "hello")

	type lm struct {
		name       string
		svcPort    int
		mountPoint string
	}
	var tests []lm
	if runtime.GOOS == "windows" {
		tests = []lm{
			{
				"one",
				80,
				"O:",
			},
			{
				"two",
				81,
				"T:",
			},
		}
	} else {
		tempDir := s.T().TempDir()
		tests = []lm{
			{
				"one",
				80,
				filepath.Join(tempDir, "one"),
			},
			{
				"two",
				81,
				filepath.Join(tempDir, "two"),
			},
		}
	}

	for i, tt := range tests {
		s.Run(tt.name, func() {
			ctx := s.Context()
			require := s.Require()
			stdout := itest.TelepresenceOk(ctx, "intercept", "hello", "--mount", tt.mountPoint, "--port", fmt.Sprintf("%d:%d", tt.svcPort, tt.svcPort))
			defer itest.TelepresenceOk(ctx, "leave", "hello")
			require.Contains(stdout, "Using Deployment hello")
			if i == 0 {
				s.CapturePodLogs(ctx, "hello", "traffic-agent", s.AppNamespace())
			} else {
				// Mounts are sometimes slow
				time.Sleep(3 * time.Second)
			}
			ns, err := os.ReadFile(filepath.Join(tt.mountPoint, "var", "run", "secrets", "kubernetes.io", "serviceaccount", "namespace"))
			require.NoError(err)
			require.Equal(s.AppNamespace(), string(ns))
			token, err := os.ReadFile(filepath.Join(tt.mountPoint, "var", "run", "secrets", "kubernetes.io", "serviceaccount", "token"))
			require.NoError(err)
			require.True(len(token) > 0)
		})
	}
}
