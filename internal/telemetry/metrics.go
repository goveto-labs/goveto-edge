package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
)

// All metrics use low-cardinality labels only: route templates, methods,
// status codes, record types. Never label with domains, paths, or IPs.
var (
	HTTPRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "goveto", Subsystem: "control_http", Name: "requests_total",
		Help: "Control-plane HTTP requests by route, method and status code.",
	}, []string{"route", "method", "code"})

	HTTPRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "goveto", Subsystem: "control_http", Name: "request_duration_seconds",
		Help:    "Control-plane HTTP request latency by route.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"route"})

	IngestBatchesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "goveto", Subsystem: "analytics_ingest", Name: "batches_total",
		Help: "Agent log batches processed by analytics ingest, by result.",
	}, []string{"result"})

	IngestRecordsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "goveto", Subsystem: "analytics_ingest", Name: "records_total",
		Help: "Agent log records processed by analytics ingest, by record type.",
	}, []string{"type"})

	IngestBatchDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "goveto", Subsystem: "analytics_ingest", Name: "batch_duration_seconds",
		Help:    "Time to consume one agent log batch.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	})

	EdgeAgentsConnected = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "goveto", Subsystem: "edge", Name: "agents_connected",
		Help: "Edge agents currently holding a management stream on this replica.",
	})
)

func init() {
	registry.MustRegister(
		HTTPRequestsTotal,
		HTTPRequestDuration,
		IngestBatchesTotal,
		IngestRecordsTotal,
		IngestBatchDuration,
		EdgeAgentsConnected,
	)
}
