// Package metrics provides Prometheus metrics for monitoring the controller.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// ReconcileTotal tracks the total number of reconciliation attempts
	ReconcileTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pangolin_gateway_reconcile_total",
			Help: "Total number of reconciliation attempts",
		},
		[]string{"controller", "result"}, // result: success, error, requeue
	)

	// ReconcileDuration tracks the time spent in reconciliation
	ReconcileDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "pangolin_gateway_reconcile_duration_seconds",
			Help:    "Time spent in reconciliation",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"controller"},
	)

	// PangolinAPIRequests tracks the total number of Pangolin API requests
	PangolinAPIRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pangolin_api_requests_total",
			Help: "Total number of Pangolin API requests",
		},
		[]string{"endpoint", "method", "status_code"},
	)

	// PangolinAPILatency tracks the latency of Pangolin API requests
	PangolinAPILatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "pangolin_api_request_duration_seconds",
			Help:    "Pangolin API request duration in seconds",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		},
		[]string{"endpoint", "method"},
	)

	// PangolinAPIErrors tracks the number of Pangolin API errors
	PangolinAPIErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pangolin_api_errors_total",
			Help: "Total number of Pangolin API errors",
		},
		[]string{"endpoint", "method", "status_code"},
	)

	// GatewayTotal tracks the total number of Gateway resources
	GatewayTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pangolin_gateway_total",
			Help: "Total number of Gateway resources managed",
		},
		[]string{"namespace"},
	)

	// HTTPRouteTotal tracks the total number of HTTPRoute resources
	HTTPRouteTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pangolin_httproute_total",
			Help: "Total number of HTTPRoute resources managed",
		},
		[]string{"namespace"},
	)

	// GRPCRouteTotal tracks the total number of GRPCRoute resources
	GRPCRouteTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pangolin_grpcroute_total",
			Help: "Total number of GRPCRoute resources managed",
		},
		[]string{"namespace"},
	)

	// PangolinSites tracks the number of Pangolin sites created
	PangolinSites = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pangolin_sites_total",
			Help: "Total number of Pangolin sites",
		},
		[]string{"online"}, // online: true/false
	)

	// PangolinResources tracks the number of Pangolin resources
	PangolinResources = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "pangolin_resources_total",
			Help: "Total number of Pangolin resources",
		},
	)

	// PangolinTargets tracks the number of Pangolin targets
	PangolinTargets = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pangolin_targets_total",
			Help: "Total number of Pangolin targets",
		},
		[]string{"enabled"}, // enabled: true/false
	)

	// CircuitBreakerState tracks the current state of the Pangolin API circuit breaker.
	// Values: 0=closed (normal), 1=open (failing fast), 2=half-open (probing).
	CircuitBreakerState = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "pangolin_circuit_breaker_state",
			Help: "Current state of the Pangolin API circuit breaker (0=closed, 1=open, 2=half-open)",
		},
	)
)

func init() {
	// Register custom metrics with the controller-runtime metrics registry
	metrics.Registry.MustRegister(
		ReconcileTotal,
		ReconcileDuration,
		PangolinAPIRequests,
		PangolinAPILatency,
		PangolinAPIErrors,
		GatewayTotal,
		HTTPRouteTotal,
		GRPCRouteTotal,
		PangolinSites,
		PangolinResources,
		PangolinTargets,
		CircuitBreakerState,
	)
}
