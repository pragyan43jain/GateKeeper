package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestTracingExportsAndPropagatesWithoutSecrets(t *testing.T) {
	payloads := make(chan []byte, 1)
	collectorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		payloads <- data
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(200)
	}))
	defer collectorServer.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collectorServer.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	oldProvider := otel.GetTracerProvider()
	oldPropagator := otel.GetTextMapPropagator()
	defer otel.SetTracerProvider(oldProvider)
	defer otel.SetTextMapPropagator(oldPropagator)
	shutdown, err := initializeTracing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	const parent = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	request := httptest.NewRequest("GET", "/api/items?secret=private-query", nil)
	request.Header.Set("traceparent", parent)
	request.Header.Set("X-API-Key", "private-key")
	handler := traceRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("traceparent") == parent {
			t.Error("outgoing context did not acquire a gateway span")
		}
		w.WriteHeader(503)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), request)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-payloads:
		var exported collector.ExportTraceServiceRequest
		if err := proto.Unmarshal(data, &exported); err != nil {
			t.Fatal(err)
		}
		if len(exported.ResourceSpans) != 1 || len(exported.ResourceSpans[0].ScopeSpans) != 1 {
			t.Fatalf("unexpected export: %v", &exported)
		}
		spans := exported.ResourceSpans[0].ScopeSpans[0].Spans
		if len(spans) != 1 || spans[0].Status.GetCode() != 2 {
			t.Fatalf("missing failed request span: %v", spans)
		}
		for _, attr := range spans[0].Attributes {
			if attr.Key != "http.request.method" && attr.Key != "http.response.status_code" {
				t.Errorf("unexpected telemetry attribute: %s", attr.Key)
			}
		}
	case <-ctx.Done():
		t.Fatal("OTLP collector received no spans")
	}
}
