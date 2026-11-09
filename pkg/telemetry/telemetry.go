// Package telemetry sets up tracing, an OTLP exporter, W3C context propagation and server spans for HTTP and gRPC
package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// Instrumentation scope of every tracer this package hands out
const scope = "github.com/nickheyer/nebu"

// The model a request names, from the GenAI semantic conventions
const AttrModel = attribute.Key("gen_ai.request.model")

// W3C trace context, carried in the traceparent and tracestate headers
var propagator = propagation.TraceContext{}

// Where traces go
type Config struct {
	// Host and port of an OTLP HTTP collector, spans going nowhere but Exporter when empty
	Endpoint    string
	ServiceName string
	// Sends plain HTTP to the collector instead of HTTPS
	Insecure bool
	// Fraction of root traces sampled from zero to one, children following their parent
	SampleRatio float64
	// Receives every sampled span as it ends, beside the collector, nil for none
	Exporter sdktrace.SpanExporter
}

// The installed tracer provider
type Provider struct {
	*sdktrace.TracerProvider
}

// Installs a global tracer provider and propagator, exporting to the collector and the extra exporter when set
func Setup(ctx context.Context, cfg Config) (*Provider, func(context.Context) error, error) {
	if cfg.ServiceName == "" {
		return nil, nil, fmt.Errorf("telemetry: service name is required")
	}
	if cfg.SampleRatio < 0 || cfg.SampleRatio > 1 {
		return nil, nil, fmt.Errorf("telemetry: sample ratio %v is outside zero to one", cfg.SampleRatio)
	}
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(cfg.ServiceName)))
	if err != nil {
		return nil, nil, fmt.Errorf("telemetry: resource: %w", err)
	}
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	}
	if cfg.Exporter != nil {
		opts = append(opts, sdktrace.WithSyncer(cfg.Exporter))
	}
	if cfg.Endpoint != "" {
		exp, err := otlpExporter(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagator)
	return &Provider{TracerProvider: tp}, tp.Shutdown, nil
}

// Builds the OTLP HTTP exporter for a host and port endpoint
func otlpExporter(ctx context.Context, cfg Config) (sdktrace.SpanExporter, error) {
	if strings.Contains(cfg.Endpoint, "://") || strings.Contains(cfg.Endpoint, "/") {
		return nil, fmt.Errorf("telemetry: endpoint %q must be host and port", cfg.Endpoint)
	}
	opts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(cfg.Endpoint)}
	if cfg.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	exp, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: otlp exporter: %w", err)
	}
	return exp, nil
}

// Starts an internal span for one step of a request, a child of any span in ctx
func StartRequestSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(scope).Start(ctx, name, trace.WithSpanKind(trace.SpanKindInternal), trace.WithAttributes(attrs...))
}

// Writes the span context of ctx into the traceparent and tracestate headers
func Inject(ctx context.Context, h http.Header) {
	propagator.Inject(ctx, propagation.HeaderCarrier(h))
}

// Reads traceparent and tracestate from the headers into a remote parent on ctx
func Extract(ctx context.Context, h http.Header) context.Context {
	return propagator.Extract(ctx, propagation.HeaderCarrier(h))
}
