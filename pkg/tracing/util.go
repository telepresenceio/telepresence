package tracing

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func RecordInterceptSpec(span trace.Span, spec *manager.InterceptSpec) {
	span.SetAttributes(
		attribute.String("service-name", spec.ServiceName),
		attribute.String("service-namespace", spec.Namespace),
		attribute.String("mechanism", spec.Mechanism),
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

func RecordConnID(span trace.Span, id string) {
	span.SetAttributes(attribute.String("conn-id", id))
}
