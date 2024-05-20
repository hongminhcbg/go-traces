package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
)

func Fibo(ctx context.Context, n int) (int, error) {
	newCtx, span := otel.Tracer("fibo").Start(ctx, "Run")
	defer span.End()
	span.SetAttributes(attribute.String("request.n", fmt.Sprint(n)))
	if n <= 1 {
		return n, nil
	}

	if n > 50 {
		return 0, errors.New("to big")
	}

	n1 := 0
	n2 := 1
	newCtx, span2 := otel.Tracer("fibo").Start(newCtx, "Calc")
	defer span2.End()
	for i := 2; i <= n; i++ {
		n2, n1 = n1+n2, n2
	}

	return n2, nil
}

// newResource returns a resource describing this application.
func newResource() *resource.Resource {
	r, _ := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("fib"),
			semconv.ServiceVersion("v0.1.0"),
			attribute.String("environment", "demo"),
		),
	)
	return r
}

// newExporter returns a console exporter.
func newExporter(w io.Writer) (trace.SpanExporter, error) {
	return stdouttrace.New(
		stdouttrace.WithWriter(w),
		// Use human-readable output.
		stdouttrace.WithPrettyPrint(),
		// Do not print timestamps for the demo.
		stdouttrace.WithoutTimestamps(),
	)
}

func setupTracing(ctx context.Context, serviceName string) (*trace.TracerProvider, error) {
	exporter, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithEndpoint("localhost:4317"),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	// labels/tags/resources that are common to all traces.
	resource := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceNameKey.String(serviceName),
		attribute.String("some-attribute", "some-value"),
	)

	provider := trace.NewTracerProvider(
		trace.WithBatcher(exporter),
		trace.WithResource(resource),
		// set the sampling rate based on the parent span to 60%
		trace.WithSampler(trace.ParentBased(trace.TraceIDRatioBased(0.6))),
	)

	otel.SetTracerProvider(provider)

	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{}, // W3C Trace Context format; https://www.w3.org/TR/trace-context/
		),
	)

	return provider, nil
}

func main() {
	cancel := make(chan os.Signal, 1)
	signal.Notify(cancel, os.Interrupt)
	// l := log.NewLoggerConfig(log.WithInstrumentationVersion("v0.1.0"))
	l := global.Logger("main")
	r := log.Record{}

	r.SetBody(log.StringValue("Hello, World!"))
	l.Emit(context.Background(), r)
	fmt.Println("Hello, World!")

	tp, err := setupTracing(context.Background(), "X")
	if err != nil {
		panic(err)
	}

	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			panic(err)
		}
	}()
	otel.SetTracerProvider(tp)

	go func() {
		for {
			var n int
			_, err := fmt.Fscanf(os.Stdin, "%d\n", &n)
			if err != nil {
				panic(err)
			}

			fmt.Println(Fibo(context.Background(), n))
		}
	}()

	select {
	case <-cancel:
		return
	}
}
