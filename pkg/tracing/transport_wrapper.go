package tracing

import (
	"context"
	"io"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.10.0"
	"go.opentelemetry.io/otel/trace"
	"k8s.io/client-go/transport"
)

// This code is modified from
// https://github.com/signalfx/splunk-otel-go/blob/main/instrumentation/k8s.io/client-go/splunkclient-go/transport/transport.go
// which is licensed under Apache 2.0
// NewWrapperFunc returns a Kubernetes WrapperFunc that can be used with a
// client configuration to trace all communication the client makes.
func NewWrapperFunc() transport.WrapperFunc {
	return func(rt http.RoundTripper) http.RoundTripper {
		if rt == nil {
			rt = http.DefaultTransport
		}

		wrapped := roundTripper{
			RoundTripper: rt,
		}

		return &wrapped
	}
}

// roundTripper wraps an http.RoundTripper's requests with a span.
type roundTripper struct {
	http.RoundTripper
}

var _ http.RoundTripper = (*roundTripper)(nil)

func (rt *roundTripper) RoundTrip(r *http.Request) (resp *http.Response, err error) {
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(semconv.HTTPClientAttributesFromHTTPRequest(r)...),
	}

	tracer := otel.GetTracerProvider().Tracer("")
	ctx, span := tracer.Start(r.Context(), "k8s."+name(r), opts...)

	// Ensure anything downstream knows about the started span.
	r = r.WithContext(ctx)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(r.Header))

	resp, err = rt.RoundTripper.RoundTrip(r)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.End()
		return
	}

	span.SetAttributes(semconv.HTTPAttributesFromHTTPStatusCode(resp.StatusCode)...)
	span.SetStatus(semconv.SpanStatusFromHTTPStatusCode(resp.StatusCode))
	resp.Body = &wrappedBody{ctx: ctx, span: span, body: resp.Body}

	return
}

const (
	prefixAPI   = "/api/v1/"
	prefixWatch = "watch/"
)

// name returns an appropriate span name based on the client request.
// OpenTelemetry semantic conventions require this name to be low cardinality,
// but since the Kubernetes API is somewhat predictable we can usually return
// more than just "HTTP {METHOD}".
func name(r *http.Request) string {
	path := r.URL.Path
	method := r.Method

	if !strings.HasPrefix(path, prefixAPI) {
		return "HTTP " + method
	}

	var out strings.Builder
	out.WriteString("HTTP " + method + " ")

	path = strings.TrimPrefix(path, prefixAPI)

	if strings.HasPrefix(path, prefixWatch) {
		path = strings.TrimPrefix(path, prefixWatch)
		out.WriteString(prefixWatch)
	}

	// For each {type}/{name}, tokenize the {name} portion.
	var previous string
	for i, part := range strings.Split(path, "/") {
		if i > 0 {
			out.WriteRune('/')
		}

		if i%2 == 0 {
			out.WriteString(part)
			previous = part
		} else {
			out.WriteString(tokenize(previous))
		}
	}

	return out.String()
}

func tokenize(k8Type string) string {
	switch k8Type {
	case "namespaces":
		return "{namespace}"
	case "proxy":
		return "{path}"
	default:
		return "{name}"
	}
}

type wrappedBody struct {
	ctx  context.Context
	span trace.Span
	body io.ReadCloser
}

var _ io.ReadCloser = (*wrappedBody)(nil)

func (wb *wrappedBody) Read(b []byte) (int, error) {
	n, err := wb.body.Read(b)
	switch err {
	case nil:
		// nothing to do here but fall through to the return
	case io.EOF:
		wb.span.End()
	default:
		wb.span.RecordError(err)
		wb.span.SetStatus(codes.Error, err.Error())
	}

	return n, err
}

func (wb *wrappedBody) Close() error {
	wb.span.End()
	return wb.body.Close()
}
