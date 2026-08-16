// Package telemetry exposes Prometheus metrics for the control plane.
package telemetry

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// registry is the process-wide metrics registry. It is intentionally not the
// prometheus default registry so tests can construct isolated registries.
var registry = prometheus.NewRegistry()

func init() {
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}

// MustRegister registers collectors with the process-wide registry, panicking
// on duplicate registration (same contract as prometheus.MustRegister).
func MustRegister(cs ...prometheus.Collector) {
	registry.MustRegister(cs...)
}

// Handler serves the process-wide registry in Prometheus text format.
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}
