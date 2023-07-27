package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	xtrace "go.opentelemetry.io/otel/trace"
)

func hello(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	span := xtrace.SpanFromContext(ctx)
	defer span.End()

	span.AddEvent("handling this...",
		xtrace.WithAttributes(attribute.String("Hello", "Mint")))
	time.Sleep(500 * time.Millisecond)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.google.com", nil)
	if err != nil {
		return
	}
	spanhttp := xtrace.SpanFromContext(ctx)
	spanhttp.SetName("httpCall")
	httpClient.Do(req)
	_, _ = io.WriteString(w, "Hello, world!\n")
	spanhttp.End()
}

var reqCnt prometheus.Counter
var httpClient http.Client

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

func init() {
	httpClient = http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}
	reqCnt = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "prod",
		Name:      "http_req_count",
		Help:      "total request to service",
		ConstLabels: map[string]string{
			"app": "basic trace",
		},
	})
}

func main() {
	kill := make(chan os.Signal, 1)
	signal.Notify(kill, syscall.SIGINT, syscall.SIGKILL, syscall.SIGTERM)
	go func() {
		select {
		case <-kill:
			log.Println("Kill app")
			os.Exit(0)
		}
	}()
	tp, err := setupTracing(context.Background(), "X")
	if err != nil {
		panic(err)
	}

	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			log.Fatal(err)
		}
	}()
	otel.SetTracerProvider(tp)

	requestDurations := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "A histogram of the HTTP request durations in seconds.",
		Buckets: prometheus.ExponentialBuckets(0.1, 1.5, 5),
	})

	// Create non-global registry.
	registry := prometheus.NewRegistry()

	// Add go runtime metrics and process collectors.
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		requestDurations,
		reqCnt,
	)

	go func() {
		for {
			r := rand.Intn(6000)
			requestDurations.(prometheus.ExemplarObserver).ObserveWithExemplar(
				float64(r)/float64(1000),
				prometheus.Labels{"TraceID": fmt.Sprint(rand.Intn(100000))},
			)

			if rand.Intn(10) < 2 {
				reqCnt.Inc()
			}

			time.Sleep(600 * time.Millisecond)
		}
	}()

	otelHandler := otelhttp.NewHandler(http.HandlerFunc(hello), "Hello")
	http.Handle("/hello", otelHandler)
	// Expose /metrics HTTP endpoint using the created custom registry.
	http.Handle(
		"/metrics", promhttp.HandlerFor(
			registry,
			promhttp.HandlerOpts{
				EnableOpenMetrics: true,
			}),
	)
	// To test: curl -H 'Accept: application/openmetrics-text' localhost:8080/metrics
	log.Fatalln(http.ListenAndServe(":8080", nil))
}
