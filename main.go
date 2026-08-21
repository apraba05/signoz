// Demo HTTP service for SigNoz: two endpoints, one fast and one artificially slow,
// exporting traces, a custom latency histogram, and structured logs over OTLP.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const serviceName = "demo-app"

var (
	tracer   trace.Tracer
	duration metric.Float64Histogram
	logger   *slog.Logger
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	endpoint := envOr("OTEL_EXPORTER_OTLP_ENDPOINT", "signoz-otel-collector.platform.svc.cluster.local:4317")
	shutdown, err := setupOTel(ctx, endpoint)
	if err != nil {
		fmt.Fprintf(os.Stderr, "otel setup: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = shutdown(context.Background()) }()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/fast", handleFast)
	mux.HandleFunc("/slow", handleSlow)

	addr := envOr("LISTEN_ADDR", ":8080")
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		logger.Info("listening", "addr", addr, "otlp", endpoint)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func handleFast(w http.ResponseWriter, r *http.Request) {
	handle(w, r, "GET /fast", 5*time.Millisecond)
}

func handleSlow(w http.ResponseWriter, r *http.Request) {
	handle(w, r, "GET /slow", 800*time.Millisecond)
}

func handle(w http.ResponseWriter, r *http.Request, spanName string, sleep time.Duration) {
	ctx, span := tracer.Start(r.Context(), spanName)
	defer span.End()

	start := time.Now()
	time.Sleep(sleep)
	elapsed := time.Since(start)

	duration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(
		attribute.String("http.route", r.URL.Path),
	))

	// Structured log on the same context → OTLP log carries this request's trace_id.
	logger.InfoContext(ctx, "request handled",
		"path", r.URL.Path,
		"duration_ms", elapsed.Milliseconds(),
		"trace_id", span.SpanContext().TraceID().String(),
	)

	w.Header().Set("Content-Type", "text/plain")
	_, _ = fmt.Fprintf(w, "%s ok in %dms\n", r.URL.Path, elapsed.Milliseconds())
}

func setupOTel(ctx context.Context, endpoint string) (func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return nil, err
	}

	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}

	traceExp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	tracer = otel.Tracer(serviceName)

	metricExp, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(5*time.Second))),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)
	meter := otel.Meter(serviceName)
	duration, err = meter.Float64Histogram(
		"demo_request_duration_seconds",
		metric.WithDescription("Handler wall time for /fast and /slow"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	logExp, err := otlploggrpc.New(ctx, otlploggrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, err
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExp)),
		sdklog.WithResource(res),
	)
	global.SetLoggerProvider(lp)
	logger = otelslog.NewLogger(serviceName)

	return func(ctx context.Context) error {
		var first error
		for _, fn := range []func(context.Context) error{tp.Shutdown, mp.Shutdown, lp.Shutdown} {
			if err := fn(ctx); err != nil && first == nil {
				first = err
			}
		}
		return first
	}, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
