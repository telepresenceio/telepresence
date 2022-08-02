package tracing

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func RecordInterceptSpec(span trace.Span, spec *manager.InterceptSpec) {
	span.SetAttributes(
		attribute.String("service-name", spec.ServiceName),
		attribute.String("service-namespace", spec.Namespace),
		attribute.String("mechanism", spec.Mechanism),
		attribute.String("name", spec.Name),
		attribute.String("workload-kind", spec.WorkloadKind),
	)
}

func RecordInterceptInfo(span trace.Span, info *manager.InterceptInfo) {
	span.SetAttributes(
		attribute.String("intercept-id", info.Id),
		attribute.String("session-id", info.ClientSession.SessionId),
		attribute.String("disposition", info.Disposition.String()),
	)
	if info.Spec != nil {
		RecordInterceptSpec(span, info.Spec)
	}
}

func EndAndRecord(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}
