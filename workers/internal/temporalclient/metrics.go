// SDK metrics: when TEMPORAL_METRICS_ADDR is set (e.g. ":9090"), the worker
// emits Temporal SDK metrics in Prometheus format and serves them at /metrics.
//
// It uses the OpenTelemetry metrics handler + the OTel Prometheus exporter,
// which produces the temporal_*_total / temporal_*_seconds_bucket names the
// official temporalio/dashboards SDK dashboard queries. When the env var is
// unset (local host dev), no metrics server starts and the SDK uses its no-op
// handler.
package temporalclient

import (
	"log"
	"net/http"
	"os"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/contrib/opentelemetry"
)

// metricsHandler returns a Prometheus-backed SDK metrics handler, or nil if
// TEMPORAL_METRICS_ADDR is unset.
func metricsHandler() client.MetricsHandler {
	addr := os.Getenv("TEMPORAL_METRICS_ADDR")
	if addr == "" {
		return nil
	}

	registry := promclient.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	if err != nil {
		log.Fatalf("metrics: prometheus exporter: %v", err)
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))

	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
		log.Printf("serving Prometheus metrics on %s/metrics", addr)
		if err := http.ListenAndServe(addr, mux); err != nil {
			log.Printf("metrics: server stopped: %v", err)
		}
	}()

	return opentelemetry.NewMetricsHandler(opentelemetry.MetricsHandlerOptions{
		Meter: provider.Meter("temporal-sdk"),
	})
}
