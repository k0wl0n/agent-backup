package telemetry

import (
	"context"
	"fmt"
	"log"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
)

// InitTelemetry initializes OpenTelemetry tracing
func InitTelemetry(ctx context.Context, serviceName, version, endpoint, apiKey string) (func(context.Context) error, error) {
	if endpoint == "" {
		log.Println("Telemetry endpoint not configured, skipping initialization")
		return func(context.Context) error { return nil }, nil
	}

	headers := map[string]string{
		"Authorization": apiKey, // HyperDX API Key usually goes here or as a special header
	}

	// If it's HyperDX, the API key is usually passed via Authorization header "Bearer <API_KEY>" or specific header
	// For generic OTLP, it depends.
	// Assuming generic OTLP HTTP

	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithHeaders(headers),
		otlptracehttp.WithInsecure(), // Use WithInsecure if local or specific config. Ideally configurable.
		// otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
			semconv.HostName(os.Getenv("HOSTNAME")),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	log.Printf("Telemetry initialized for %s (Endpoint: %s)", serviceName, endpoint)

	return tracerProvider.Shutdown, nil
}
