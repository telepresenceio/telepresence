package integration_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/go-json-experiment/json"
	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

type largeFilesSuite struct {
	itest.Suite
	itest.TrafficManager
	name         string
	manifests    [][3]itest.TplResource
	serviceCount int
	mountPoint   []string
	largeFiles   []string
}

func (s *largeFilesSuite) SuiteName() string {
	return "LargeFiles"
}

const (
	svcCount        = 4
	fileSize        = 100 * 1024 * 1024
	fileCountPerSvc = 3
)

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &largeFilesSuite{
			Suite:          itest.Suite{Harness: h},
			TrafficManager: h,
			name:           "hello",
			serviceCount:   svcCount,
			mountPoint:     make([]string, svcCount),
			largeFiles:     make([]string, svcCount*fileCountPerSvc),
		}
	})
}

func (s *largeFilesSuite) Name() string {
	return s.name
}

func (s *largeFilesSuite) ServiceCount() int {
	return s.serviceCount
}

func (s *largeFilesSuite) SetupSuite() {
	if !(s.ManagerIsVersion(">2.21.x") && s.ClientIsVersion(">2.24.x")) {
		s.T().Skip("Not part of compatibility tests. Not enough transfer stability")
	}
	s.Suite.SetupSuite()
	ctx := s.Context()

	s.manifests = make([][3]itest.TplResource, s.ServiceCount())
	wg := sync.WaitGroup{}
	wg.Add(s.ServiceCount())
	for i := 0; i < s.ServiceCount(); i++ {
		go func() {
			defer wg.Done()
			pvName := fmt.Sprintf("%s-pv-%d", s.Name(), i)
			pv := &itest.PersistentVolume{
				Name: pvName,
			}
			if s.UseLocalPathProvisioner() {
				pv.Annotations = map[string]string{
					"pv.kubernetes.io/provisioned-by": "rancher.io/local-path",
				}
				pv.StorageClassName = "local-path"
			}
			s.NoError(pv.Apply(ctx, s.AppNamespace()))

			pvcName := fmt.Sprintf("%s-pvc-%d", s.Name(), i)
			pvc := &itest.PersistentVolumeClaim{
				Name: pvcName,
			}
			if s.UseLocalPathProvisioner() {
				pvc.Annotations = map[string]string{
					"pv.kubernetes.io/provisioned-by": "rancher.io/local-path",
				}
				pvc.StorageClassName = "local-path"
			}
			s.NoError(pvc.Apply(ctx, s.AppNamespace()))

			svc := fmt.Sprintf("%s-%d", s.Name(), i)
			dep := &itest.Generic{
				Name:     svc,
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
						MountPath: "/home/scratch",
					},
				},
			}
			s.NoError(dep.Apply(ctx, s.AppNamespace()))
			s.NoError(itest.RolloutStatusWait(ctx, s.AppNamespace(), "deploy/"+svc))
			s.manifests[i] = [3]itest.TplResource{dep, pvc, pv}
		}()
	}
	wg.Wait()
}

func (s *largeFilesSuite) TearDownSuite() {
	ctx := s.Context()
	// Delete in reverse order
	wg := sync.WaitGroup{}
	wg.Add(s.ServiceCount())
	for _, trs := range s.manifests {
		go func() {
			defer wg.Done()
			for _, tr := range trs {
				s.NoError(tr.Delete(ctx))
			}
		}()
	}
	wg.Wait()
	itest.TelepresenceQuitOk(ctx)
}

func (s *largeFilesSuite) createIntercepts(ctx context.Context) {
	s.TelepresenceConnect(ctx)

	wg := sync.WaitGroup{}
	wg.Add(s.ServiceCount())
	for i := 0; i < s.ServiceCount(); i++ {
		go func(i int) {
			defer wg.Done()
			svc := fmt.Sprintf("%s-%d", s.Name(), i)
			stdout := itest.TelepresenceOk(ctx, "intercept",
				"--detailed-output",
				"--output", "json",
				"--port", strconv.Itoa(8080+i),
				svc,
			)
			var info intercept.Info
			require := s.Require()
			require.NoError(json.Unmarshal([]byte(stdout), &info))
			require.Equal(svc, info.Name, ioutil.WriterToString(info.WriteTo))
			require.NotNil(info.Mount)
			s.mountPoint[i] = info.Mount.LocalDir
			s.NoError(itest.RolloutStatusWait(ctx, s.AppNamespace(), "deploy/"+svc))
			s.CapturePodLogs(ctx, svc, "traffic-agent", s.AppNamespace())
		}(i)
	}
	wg.Wait()
	time.Sleep(7 * time.Second)
}

func (s *largeFilesSuite) leaveIntercepts(ctx context.Context) {
	for i := 0; i < s.ServiceCount(); i++ {
		itest.TelepresenceOk(ctx, "leave", fmt.Sprintf("%s-%d", s.Name(), i))
	}
}

func (s *largeFilesSuite) Test_LargeFileIntercepts_fuseftp() {
	ctx := itest.WithConfig(s.Context(), func(cfg client.Config) {
		cfg.Timeouts().PrivateFtpReadWrite = 2 * time.Minute
		cfg.Timeouts().PrivateFtpShutdown = 3 * time.Minute
		cfg.Intercept().UseFtp = true
	})
	s.largeFileIntercepts(ctx)
}

func (s *largeFilesSuite) Test_LargeFileIntercepts_sshfs() {
	ctx := itest.WithConfig(s.Context(), func(cfg client.Config) {
		cfg.Intercept().UseFtp = false
	})
	s.largeFileIntercepts(ctx)
}

func (s *largeFilesSuite) largeFileIntercepts(ctx context.Context) {
	s.createIntercepts(ctx)
	wg := sync.WaitGroup{}

	// Start by creating files in the mounted filesystem from entry 1 - fileCountPerSvc for each service.
	// We leave the first entry empty because in the next step, we want to create a file parallel to
	// validating the ones we create here so that there is heavy parallel reads and writes.
	wg.Add(s.ServiceCount() * (fileCountPerSvc - 1))
	for i := 0; i < s.ServiceCount(); i++ {
		for n := 1; n < fileCountPerSvc; n++ { // Leave the first entry empty for now
			go func(i, n int) {
				defer wg.Done()
				path, err := s.createLargeFile(filepath.Join(s.mountPoint[i], "home", "scratch"), fileSize)
				s.largeFiles[i*fileCountPerSvc+n] = filepath.Base(path)
				s.NoError(err)
			}(i, n)
		}
	}
	wg.Wait()

	// At this point we leave the intercepts so that all directories are unmounted. The volumes are persistent
	// so they will be remounted.
	s.leaveIntercepts(ctx)
	if s.T().Failed() {
		s.T().FailNow()
	}
	s.createIntercepts(ctx)

	// Parallel to creating the first entry, also validate the ones that we created in step 1.
	wg.Add(s.ServiceCount() * fileCountPerSvc)
	for i := 0; i < s.ServiceCount(); i++ {
		go func(i int) {
			defer wg.Done()
			path, err := s.createLargeFile(filepath.Join(s.mountPoint[i], "home", "scratch"), fileSize)
			s.largeFiles[i*fileCountPerSvc] = filepath.Base(path)
			s.NoError(err)
		}(i)
		for n := 1; n < fileCountPerSvc; n++ { // Leave the first entry empty for now
			go func(i, n int) {
				defer wg.Done()
				s.NoError(itest.TimedRun(ctx, 10*time.Second, func(_ context.Context) error {
					return validateLargeFile(filepath.Join(s.mountPoint[i], "home", "scratch", s.largeFiles[i*fileCountPerSvc+n]), fileSize)
				}))
			}(i, n)
		}
	}
	wg.Wait()
	s.leaveIntercepts(ctx)
	if s.T().Failed() {
		s.T().FailNow()
	}
	s.createIntercepts(ctx)
	defer s.leaveIntercepts(ctx)

	// Validate the first entry
	wg.Add(s.ServiceCount())
	for i := 0; i < s.ServiceCount(); i++ {
		go func(i int) {
			defer wg.Done()
			s.NoError(itest.TimedRun(ctx, 10*time.Second, func(_ context.Context) error {
				return validateLargeFile(filepath.Join(s.mountPoint[i], "home", "scratch", s.largeFiles[i*fileCountPerSvc]), fileSize)
			}))
		}(i)
	}
	wg.Wait()
}

func (s *largeFilesSuite) createLargeFile(dir string, sz int) (string, error) {
	if sz%4 != 0 {
		return "", errors.New("size%4 must be zero")
	}
	qsz := sz / 4 // We'll write a sequence of uint32 values
	if qsz > math.MaxUint32 {
		return "", fmt.Errorf("size must be less than %d", math.MaxUint32*4)
	}
	f, err := os.CreateTemp(dir, "big-*.bin")
	if err != nil {
		return "", fmt.Errorf("%s: os.CreateTemp failed: %w", time.Now().Format("15:04:05.0000"), err)
	}
	defer f.Close()
	bf := bufio.NewWriter(f)

	qz := uint32(qsz)
	buf := make([]byte, 4)
	for i := uint32(0); i < qz; i++ {
		binary.BigEndian.PutUint32(buf, i)
		n, err := bf.Write(buf)
		if err != nil {
			return "", fmt.Errorf("%s: Write on %s failed: %w", time.Now().Format("15:04:05.0000"), f.Name(), err)
		}
		if n != 4 {
			return "", errors.New("didn't write quartet")
		}
	}
	if err := bf.Flush(); err != nil {
		return "", fmt.Errorf("%s: Flush on %s failed: %w", time.Now().Format("15:04:05.0000"), f.Name(), err)
	}
	return f.Name(), nil
}

func validateLargeFile(name string, sz int) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() != int64(sz) {
		return fmt.Errorf("file size differ. Expected %d, got %d", sz, st.Size())
	}
	bf := bufio.NewReader(f)
	qz := uint32(sz / 4)
	buf := make([]byte, 4)
	for i := uint32(0); i < qz; i++ {
		n, err := bf.Read(buf)
		if err != nil {
			return err
		}
		if n != 4 {
			return errors.New("didn't read quartet")
		}
		x := binary.BigEndian.Uint32(buf)
		if i != x {
			return fmt.Errorf("content differ at position %d: expected %d, got %d", i*4, i, x)
		}
	}
	return nil
}
