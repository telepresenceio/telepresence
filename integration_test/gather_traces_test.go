package integration_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/klauspost/compress/gzip"
	semconv "go.opentelemetry.io/otel/semconv/v1.10.0"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
)

type gatherTracesSuite struct {
	itest.Suite
	itest.NamespacePair
	name         string
	serviceCount int
}

func (s *gatherTracesSuite) SuiteName() string {
	return "MultipleIntercepts"
}

func init() {
	itest.AddNamespacePairSuite("traces", func(h itest.NamespacePair) itest.TestingSuite {
		const serviceCount = 3
		return &gatherTracesSuite{
			Suite:         itest.Suite{Harness: h},
			NamespacePair: h,
			name:          "hello",
			serviceCount:  serviceCount,
		}
	})
}

func (s *gatherTracesSuite) SetupSuite() {
	s.Suite.SetupSuite()
	ctx := s.Context()
	wg := sync.WaitGroup{}
	wg.Add(s.serviceCount + 1)
	for i := 0; i < s.serviceCount; i++ {
		go func(i int) {
			defer wg.Done()
			s.ApplyEchoService(ctx, fmt.Sprintf("%s-%d", s.name, i), 80)
		}(i)
	}

	go func() {
		defer wg.Done()
		s.NoError(s.TelepresenceHelmInstall(ctx, false, "--set", "tracing.grpcPort=15766"))
	}()
	wg.Wait()
}

func (s *gatherTracesSuite) TearDownSuite() {
	ctx := s.Context()
	wg := sync.WaitGroup{}
	wg.Add(s.serviceCount + 1)
	for i := 0; i < s.serviceCount; i++ {
		go func(i int) {
			defer wg.Done()
			s.DeleteSvcAndWorkload(ctx, "deploy", fmt.Sprintf("hello-%d", i))
		}(i)
	}
	go func() {
		defer wg.Done()
		s.UninstallTrafficManager(ctx, s.ManagerNamespace())
	}()
	wg.Wait()
}

func (s *gatherTracesSuite) Test_GatherTraces() {
	ctx := s.Context()

	servicePort := make([]int, s.serviceCount)
	serviceCancel := make([]context.CancelFunc, s.serviceCount)

	wg := sync.WaitGroup{}
	for i := 0; i < s.serviceCount; i++ {
		servicePort[i], serviceCancel[i] = itest.StartLocalHttpEchoServer(ctx, fmt.Sprintf("%s-%d", s.name, i))
	}
	defer func() {
		for i := 0; i < s.serviceCount; i++ {
			serviceCancel[i]()
		}
	}()

	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceQuitOk(ctx)

	wg.Add(s.serviceCount)
	for i := 0; i < s.serviceCount; i++ {
		go func(i int) {
			defer wg.Done()
			svc := fmt.Sprintf("%s-%d", s.name, i)
			stdout := itest.TelepresenceOk(ctx, "intercept", svc, "--mount", "false", "--port", strconv.Itoa(servicePort[i]))
			s.Contains(stdout, "Using Deployment "+svc)
			s.NoError(s.RolloutStatusWait(ctx, "deploy/"+svc))
		}(i)
	}
	wg.Wait()

	require := s.Require()
	outputDir := s.T().TempDir()

	outputFile := filepath.Join(outputDir, "traces.gz")
	s.cleanLogDir(ctx)

	require.NoError(s.TelepresenceHelmInstall(ctx, true, "--set", "tracing.grpcPort=15766"))
	defer s.RollbackTM(ctx)

	itest.TelepresenceOk(ctx, "gather-traces", "--output-file", outputFile)
	f, err := os.Open(outputFile)
	require.NoError(err)
	defer f.Close()
	gz, err := gzip.NewReader(f)
	require.NoError(err)
	defer gz.Close()
	reader := tracing.NewProtoReader(gz, func() *tracepb.ResourceSpans { return new(tracepb.ResourceSpans) })
	traces, err := reader.ReadAll(ctx)
	require.NoError(err)
	services := map[string]struct{}{}
	for _, t := range traces {
		attrs := t.Resource.GetAttributes()
		for _, attr := range attrs {
			if attr.Key == string(semconv.ServiceNameKey) {
				services[attr.Value.GetStringValue()] = struct{}{}
			}
		}
	}
	require.Contains(services, "traffic-manager")
	require.Contains(services, "traffic-agent")
	require.Contains(services, "user-daemon")
	require.Contains(services, "root-daemon")
}

func (s *gatherTracesSuite) cleanLogDir(ctx context.Context) {
	itest.CleanLogDir(ctx, s.Require(), s.AppNamespace(), s.ManagerNamespace(), fmt.Sprintf("%s-[0-%d]", s.name, s.serviceCount))
}
