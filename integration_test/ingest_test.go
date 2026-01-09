package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/go-json-experiment/json"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ingest"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

type ingestSuite struct {
	itest.Suite
	itest.TrafficManager
	mounts []string
}

func (s *ingestSuite) SuiteName() string {
	return "Ingest"
}

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &ingestSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h}
	})
}

func (s *ingestSuite) SetupSuite() {
	ctx := s.Context()
	s.Suite.SetupSuite()
	wg := sync.WaitGroup{}
	wg.Add(3)
	go func() {
		defer wg.Done()
		s.TelepresenceHelmInstallOK(ctx, true, "--set", "intercept.environment.excluded={DATABASE_HOST,DATABASE_PASSWORD}")
	}()
	go func() {
		defer wg.Done()
		itest.ApplyAppTemplate(ctx, s.AppNamespace(), &itest.AppData{
			AppName: "echo-env",
			Image:   "ghcr.io/telepresenceio/echo-server:latest",
			Ports: []itest.AppPort{
				{
					ServicePortNumber: 80,
					TargetPortName:    "http",
					TargetPortNumber:  8080,
				},
			},
			Env: map[string]string{
				"PORT":              "8080",
				"TEST":              "DATA",
				"INTERCEPT":         "ENV",
				"DATABASE_HOST":     "HOST_NAME",
				"DATABASE_PASSWORD": "SUPER_SECRET_PASSWORD",
			},
		})
	}()
	go func() {
		defer wg.Done()
		itest.ApplyAppTemplate(ctx, s.AppNamespace(), &itest.AppData{
			AppName: "echo",
			Image:   "ghcr.io/telepresenceio/echo-server:latest",
			Ports: []itest.AppPort{
				{
					ServicePortNumber: 80,
					TargetPortName:    "http",
					TargetPortNumber:  8080,
				},
			},
			Env: map[string]string{"PORT": "8080"},
		})
	}()
	wg.Wait()
}

func (s *ingestSuite) TearDownSuite() {
	ctx := s.Context()
	wg := sync.WaitGroup{}
	wg.Add(3)
	go func() {
		defer wg.Done()
		s.DeleteSvcAndWorkload(ctx, "deploy", "echo")
	}()
	go func() {
		defer wg.Done()
		s.DeleteSvcAndWorkload(ctx, "deploy", "echo-env")
	}()
	go func() {
		defer wg.Done()
		s.RollbackTM(ctx)
	}()
	wg.Wait()
	for _, mount := range s.mounts {
		go func() {
			time.Sleep(time.Second)
			_ = os.RemoveAll(mount)
		}()
	}
}

func (s *ingestSuite) mountPoint() string {
	switch runtime.GOOS {
	case "windows":
		return "T:"
	case "darwin":
		if s.IsCI() {
			// Run without mounting on darwin. Apple prevents proper install of kernel extensions
			return "false"
		}
		fallthrough
	default:
		mountPoint, err := os.MkdirTemp("", "mount-") // Don't use the testing.Tempdir() because deletion is delayed.
		s.Require().NoError(err)
		s.mounts = append(s.mounts, mountPoint)
		return mountPoint
	}
}

func (s *ingestSuite) Test_IngestCLI() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceDisconnectOk(ctx)

	mountPoint := s.mountPoint()
	js := itest.TelepresenceOk(ctx, "ingest", "--mount", mountPoint, "echo-env", "--output", "json")
	clog.Info(ctx, js)
	var rsp ingest.Info
	s.Require().NoError(json.Unmarshal([]byte(js), &rsp))
	env := rsp.Environment
	s.Empty(env["DATABASE_HOST"])
	s.Empty(env["DATABASE_PASSWORD"])
	s.Equal("DATA", env["TEST"])
	s.Contains("ENV", env["INTERCEPT"])

	if mountPoint != "false" {
		testDir := filepath.Join(rsp.Mount.LocalDir, "var")
		s.Eventually(func() bool {
			st, err := os.Stat(testDir)
			return err == nil && st.Mode().IsDir()
		}, 15*time.Second, 3*time.Second)
	}
}

func (s *ingestSuite) Test_IngestIngestConflict() {
	mountPoint := s.mountPoint()
	if mountPoint == "false" {
		s.T().Skip("mounts disabled on this platform")
	}
	ctx := s.Context()
	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceDisconnectOk(ctx)

	itest.TelepresenceOk(ctx, "ingest", "--mount", mountPoint, "echo-env")
	so, se, err := itest.Telepresence(ctx, "ingest", "--mount", mountPoint, "echo")
	s.Require().Error(err)
	s.Empty(so)
	s.Contains(se, "already in use by ingest")
}

func (s *ingestSuite) Test_IngestInterceptConflict() {
	mountPoint := s.mountPoint()
	if mountPoint == "false" {
		s.T().Skip("mounts disabled on this platform")
	}
	ctx := s.Context()
	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceDisconnectOk(ctx)

	itest.TelepresenceOk(ctx, "ingest", "--mount", mountPoint, "echo-env")
	so, se, err := itest.Telepresence(ctx, "intercept", "--mount", mountPoint, "echo")
	s.Require().Error(err)
	s.Empty(so)
	s.Contains(se, "already in use by ingest")
}

func (s *ingestSuite) Test_InterceptIngestConflict() {
	mountPoint := s.mountPoint()
	if mountPoint == "false" {
		s.T().Skip("mounts disabled on this platform")
	}
	ctx := s.Context()
	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceDisconnectOk(ctx)

	itest.TelepresenceOk(ctx, "intercept", "--mount", mountPoint, "echo-env")
	so, se, err := itest.Telepresence(ctx, "ingest", "--mount", mountPoint, "echo")
	s.Require().Error(err)
	s.Empty(so)
	s.Contains(se, "already in use by intercept")
}

func (s *ingestSuite) Test_IngestRepeat() {
	mountPoint := s.mountPoint()
	ctx := s.Context()
	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceDisconnectOk(ctx)

	i1 := itest.TelepresenceOk(ctx, "ingest", "--mount", mountPoint, "echo-env", "--output", "json")
	i2 := itest.TelepresenceOk(ctx, "ingest", "--mount", mountPoint, "echo-env", "--output", "json")
	s.Equal(i1, i2)
}

func (s *ingestSuite) Test_IngestFTP() {
	if !s.ClientVersion().EQ(version.Structured) {
		s.T().Skip(`Not part of compatibility tests. DoWithSession assumes compiled executable`)
	}
	mountPoint := filepath.Join(s.T().TempDir(), "mnt")
	rq := s.Require()
	rq.NoError(os.Mkdir(mountPoint, 0o755))

	ctx := s.Context()
	rq.NoError(s.DoWithSession(ctx, s.NewConnectRequest(ctx), func(ctx context.Context, svc connector.ConnectorServer) {
		rsp, err := svc.Ingest(ctx, &connector.IngestRequest{
			MountPoint: mountPoint,
			Identifier: &connector.IngestIdentifier{
				WorkloadName: "echo-env",
			},
		})
		rq.NoError(err)
		env := rsp.Environment
		s.Empty(env["DATABASE_HOST"])
		s.Empty(env["DATABASE_PASSWORD"])
		s.Equal("DATA", env["TEST"])
		s.Contains("ENV", env["INTERCEPT"])

		testDir := filepath.Join(rsp.ClientMountPoint, "var")
		s.Eventually(func() bool {
			st, err := os.Stat(testDir)
			return err == nil && st.Mode().IsDir()
		}, 15*time.Second, 3*time.Second)

		_, err = svc.LeaveIngest(ctx, &connector.IngestIdentifier{
			WorkloadName: "echo-env",
		})
		rq.NoError(err)
	}))
}

func (s *ingestSuite) Test_IngestProxyVia() {
	ctx := s.Context()
	cr := s.NewConnectRequest(ctx)

	// Simulate --proxy-via all=echo-easy
	cr.SubnetViaWorkloads = []*daemon.SubnetViaWorkload{
		{
			Subnet:   "also",
			Workload: "echo-env",
		},
		{
			Subnet:   "service",
			Workload: "echo-env",
		},
		{
			Subnet:   "pods",
			Workload: "echo-env",
		},
	}

	ctx = itest.WithConfig(ctx, func(cfg client.Config) {
		// We currently have no way to make proxy-via work with FTP, because FTP
		// sends an IP-address in a TCP message.
		cfg.Intercept().UseFtp = false
	})

	mountPoint := filepath.Join(s.T().TempDir(), "mnt")
	rq := s.Require()
	rq.NoError(os.Mkdir(mountPoint, 0o755))
	rq.NoError(s.DoWithSession(ctx, cr, func(ctx context.Context, svc connector.ConnectorServer) {
		rsp, err := svc.Ingest(ctx, &connector.IngestRequest{
			MountPoint: mountPoint,
			Identifier: &connector.IngestIdentifier{
				WorkloadName: "echo-env",
			},
		})
		rq.NoError(err)
		env := rsp.Environment
		s.Empty(env["DATABASE_HOST"])
		s.Empty(env["DATABASE_PASSWORD"])
		s.Equal("DATA", env["TEST"])
		s.Contains("ENV", env["INTERCEPT"])

		testDir := filepath.Join(rsp.ClientMountPoint, "var")
		s.Eventually(func() bool {
			st, err := os.Stat(testDir)
			return err == nil && st.Mode().IsDir()
		}, 15*time.Second, 3*time.Second)

		_, err = svc.LeaveIngest(ctx, &connector.IngestIdentifier{
			WorkloadName: "echo-env",
		})
		rq.NoError(err)
	}))
}
