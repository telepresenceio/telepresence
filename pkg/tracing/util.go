package tracing

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func RecordInterceptSpec(span trace.Span, spec *manager.InterceptSpec) {
	span.SetAttributes(
		attribute.String("tel2.service-name", spec.ServiceName),
		attribute.String("tel2.service-namespace", spec.Namespace),
		attribute.String("tel2.mechanism", spec.Mechanism),
		attribute.String("tel2.intercept-name", spec.Name),
		attribute.String("tel2.agent-name", spec.Agent),
		attribute.String("tel2.workload-kind", spec.WorkloadKind),
	)
}

func RecordInterceptInfo(span trace.Span, info *manager.InterceptInfo) {
	span.SetAttributes(
		attribute.String("tel2.intercept-id", info.Id),
		attribute.String("tel2.session-id", info.ClientSession.SessionId),
		attribute.String("tel2.disposition", info.Disposition.String()),
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
