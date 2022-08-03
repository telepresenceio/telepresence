package integration_test

import (
	"os"
	"path/filepath"

	"github.com/klauspost/compress/gzip"
	semconv "go.opentelemetry.io/otel/semconv/v1.10.0"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
)

func (s *multipleInterceptsSuite) TestGatherTraces() {
	require := s.Require()
	outputDir := s.T().TempDir()
	ctx := s.Context()
	outputFile := filepath.Join(outputDir, "traces.gz")
	s.cleanLogDir(ctx)
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
