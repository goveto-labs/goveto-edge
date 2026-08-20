package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Alerting metrics. Labels stay low-cardinality: rule kinds and alert
// severities are a closed set, channel services are bounded by the
// operator-configured channels.
var (
	AlertInstances = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "goveto", Subsystem: "alerts", Name: "instances",
		Help: "Alert instances by status.",
	}, []string{"status"})

	AlertEvaluationsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "goveto", Subsystem: "alerts", Name: "evaluations_total",
		Help: "Alert rule evaluations by kind and result.",
	}, []string{"kind", "result"})

	AlertStateTransitionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "goveto", Subsystem: "alerts", Name: "state_transitions_total",
		Help: "Alert instance state transitions.",
	}, []string{"from", "to"})

	AlertNotificationsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "goveto", Subsystem: "alerts", Name: "notifications_total",
		Help: "Alert notification deliveries by rule kind, channel service and result.",
	}, []string{"kind", "service", "result"})
)

func init() {
	MustRegister(
		AlertInstances,
		AlertEvaluationsTotal,
		AlertStateTransitionsTotal,
		AlertNotificationsTotal,
	)
}
