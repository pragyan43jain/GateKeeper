package main

import (
	"context"
	"net/http"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Export is opt-in; the exporter reads standard OTLP endpoint and TLS settings.
func initializeTracing(ctx context.Context) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0.1))),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "gatekeeper-gateway"))),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

func traceRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		propagator := propagation.TraceContext{}
		ctx := propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer("gatekeeper/gateway").Start(ctx, "gateway.request", trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attribute.String("http.request.method", r.Method)))
		defer span.End()
		request := r.Clone(ctx)
		propagator.Inject(ctx, propagation.HeaderCarrier(request.Header))
		response := newStatusResponseWriter(w)
		next.ServeHTTP(response, request)
		span.SetAttributes(attribute.Int("http.response.status_code", response.statusCode))
		if response.statusCode >= 500 {
			span.SetStatus(codes.Error, "gateway request failed")
		}
	})
}
