package tracing

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.10.0"
	"go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
)

func setupTracer(ctx context.Context, componentName string, client otlptrace.Client, extraAttributes ...attribute.KeyValue) (*tracesdk.TracerProvider, error) {
	exp, err := otlptrace.New(ctx, client)
	if err != nil {
		return nil, err
	}
	r, err := resource.New(ctx,
		// We use these instead of resource.WithProcess() because the ProcessOwner detector
		// can break when running as a user without a username (e.g. UID 1000)
		resource.WithProcessCommandArgs(),
		resource.WithProcessExecutableName(),
		resource.WithProcessExecutablePath(),
		resource.WithProcessPID(),
		resource.WithProcessRuntimeDescription(),
		resource.WithProcessRuntimeName(),
		resource.WithProcessRuntimeVersion(),
		resource.WithContainer(),
		resource.WithHost(),
		resource.WithOS(),
		resource.WithAttributes(semconv.ServiceNameKey.String(componentName)),
		resource.WithAttributes(extraAttributes...),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, err
	}
	tp := tracesdk.NewTracerProvider(
		// Always be sure to batch in production.
		tracesdk.WithBatcher(exp),
		tracesdk.WithSampler(tracesdk.AlwaysSample()),
		// Record information about this application in a Resource.
		tracesdk.WithResource(r),
	)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	otel.SetTracerProvider(tp)
	return tp, nil
}

type TraceServer struct {
	common.UnimplementedTracingServer
	shim *otlpShim
	tp   *tracesdk.TracerProvider
}

func NewTraceServer(ctx context.Context, componentName string, extraAttributes ...attribute.KeyValue) (*TraceServer, error) {
	client := &otlpShim{}
	tp, err := setupTracer(ctx, componentName, client, extraAttributes...)
	if err != nil {
		return nil, err
	}

	return &TraceServer{
		tp:   tp,
		shim: client,
	}, nil
}

func (ts *TraceServer) Shutdown(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()
	if err := ts.tp.Shutdown(ctx); err != nil {
		dlog.Error(ctx, "error shutting down tracer: ", err)
	}
	otel.SetTracerProvider(noop.NewTracerProvider())
}

func (ts *TraceServer) ServeGrpc(ctx context.Context, port uint16) error {
	opts := []grpc.ServerOption{}
	grpcHandler := grpc.NewServer(opts...)
	sc := &dhttp.ServerConfig{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
				grpcHandler.ServeHTTP(w, r)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		}),
	}

	common.RegisterTracingServer(grpcHandler, ts)

	return sc.ListenAndServe(ctx, fmt.Sprintf("0.0.0.0:%d", port))
}

func (ts *TraceServer) DumpTraces(ctx context.Context, _ *emptypb.Empty) (*common.Trace, error) {
	err := ts.tp.ForceFlush(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to force flush tracer: %w", err)
	}
	b := ts.shim.dumpTraces()
	return &common.Trace{
		TraceData: b,
	}, nil
}
