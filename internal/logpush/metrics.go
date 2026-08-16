package logpush

import (
	"github.com/prometheus/client_golang/prometheus"

	"goveto-edge/internal/telemetry"
)

var (
	recordsEnqueued = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "goveto", Subsystem: "logpush", Name: "records_enqueued_total",
		Help: "Records accepted into a logpush destination queue.",
	}, []string{"cluster_id", "destination_id", "destination"})

	recordsDelivered = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "goveto", Subsystem: "logpush", Name: "records_delivered_total",
		Help: "Records successfully produced to Kafka.",
	}, []string{"cluster_id", "destination_id", "destination"})

	recordsDropped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "goveto", Subsystem: "logpush", Name: "records_dropped_total",
		Help: "Records dropped before or during Kafka produce, by reason.",
	}, []string{"cluster_id", "destination_id", "destination", "reason"})

	queueDepth = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "goveto", Subsystem: "logpush", Name: "queue_depth",
		Help: "Records currently buffered in a destination's delivery queue.",
	}, []string{"cluster_id", "destination_id", "destination"})

	destinationUp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "goveto", Subsystem: "logpush", Name: "destination_up",
		Help: "1 when the most recent produce to the destination succeeded.",
	}, []string{"cluster_id", "destination_id", "destination"})
)

func init() {
	telemetry.MustRegister(recordsEnqueued, recordsDelivered, recordsDropped, queueDepth, destinationUp)
}

func destinationLabels(dest Destination) []string {
	return []string{dest.ClusterID, dest.ID, dest.Name}
}

func initializeDestinationMetrics(dest Destination) {
	labels := destinationLabels(dest)
	queueDepth.WithLabelValues(labels...).Set(0)
	destinationUp.WithLabelValues(labels...).Set(0)
}

func forgetDestinationMetrics(dest Destination) {
	labels := destinationLabels(dest)
	recordsEnqueued.DeleteLabelValues(labels...)
	recordsDelivered.DeleteLabelValues(labels...)
	recordsDropped.DeletePartialMatch(prometheus.Labels{
		"cluster_id": dest.ClusterID, "destination_id": dest.ID, "destination": dest.Name,
	})
	queueDepth.DeleteLabelValues(labels...)
	destinationUp.DeleteLabelValues(labels...)
}
