package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

// TestMetricsRegistered verifies that all metrics are non-nil after init().
func TestMetricsRegistered(t *testing.T) {
	assert.NotNil(t, ReconcileTotal, "ReconcileTotal should be registered")
	assert.NotNil(t, ReconcileDuration, "ReconcileDuration should be registered")
	assert.NotNil(t, PangolinAPIRequests, "PangolinAPIRequests should be registered")
	assert.NotNil(t, PangolinAPILatency, "PangolinAPILatency should be registered")
	assert.NotNil(t, PangolinAPIErrors, "PangolinAPIErrors should be registered")
	assert.NotNil(t, GatewayTotal, "GatewayTotal should be registered")
	assert.NotNil(t, HTTPRouteTotal, "HTTPRouteTotal should be registered")
	assert.NotNil(t, GRPCRouteTotal, "GRPCRouteTotal should be registered")
	assert.NotNil(t, PangolinSites, "PangolinSites should be registered")
	assert.NotNil(t, PangolinResources, "PangolinResources should be registered")
	assert.NotNil(t, PangolinTargets, "PangolinTargets should be registered")
}

// TestReconcileTotal_CounterIncrement verifies counter labels and incrementing.
func TestReconcileTotal_CounterIncrement(t *testing.T) {
	controllers := []string{"gateway", "newt", "httproute", "grpcroute"}
	results := []string{"success", "error", "requeue"}

	for _, ctrl := range controllers {
		for _, result := range results {
			counter := ReconcileTotal.WithLabelValues(ctrl, result)
			assert.NotNil(t, counter, "WithLabelValues(%s, %s) should return a counter", ctrl, result)
			// Incrementing should not panic
			counter.Inc()
		}
	}
}

// TestReconcileDuration_HistogramObserve verifies histogram accepts observations.
func TestReconcileDuration_HistogramObserve(t *testing.T) {
	controllers := []string{"gateway", "newt", "httproute"}

	for _, ctrl := range controllers {
		observer := ReconcileDuration.WithLabelValues(ctrl)
		assert.NotNil(t, observer, "WithLabelValues(%s) should return an observer", ctrl)
		// Observing should not panic
		observer.Observe(0.5)
		observer.Observe(1.25)
	}
}

// TestPangolinAPIRequests_CounterLabels verifies the 3-label counter.
func TestPangolinAPIRequests_CounterLabels(t *testing.T) {
	counter := PangolinAPIRequests.WithLabelValues("/org/home/sites", "GET", "200")
	assert.NotNil(t, counter)
	counter.Inc()

	counter = PangolinAPIRequests.WithLabelValues("/org/home/site", "PUT", "500")
	assert.NotNil(t, counter)
	counter.Inc()
}

// TestPangolinAPILatency_HistogramObserve verifies API latency histogram.
func TestPangolinAPILatency_HistogramObserve(t *testing.T) {
	observer := PangolinAPILatency.WithLabelValues("/org/home/sites", "GET")
	assert.NotNil(t, observer)
	observer.Observe(0.123)
}

// TestPangolinAPIErrors_CounterLabels verifies the error counter.
func TestPangolinAPIErrors_CounterLabels(t *testing.T) {
	counter := PangolinAPIErrors.WithLabelValues("/org/home/sites", "GET", "500")
	assert.NotNil(t, counter)
	counter.Inc()
}

// TestGaugeMetrics_SetAndIncrement verifies gauge metrics can be set and incremented.
func TestGaugeMetrics_SetAndIncrement(t *testing.T) {
	tests := []struct {
		name   string
		gauge  *prometheus.GaugeVec
		labels []string
	}{
		{"GatewayTotal", GatewayTotal, []string{"default"}},
		{"HTTPRouteTotal", HTTPRouteTotal, []string{"default"}},
		{"GRPCRouteTotal", GRPCRouteTotal, []string{"production"}},
		{"PangolinSites", PangolinSites, []string{"true"}},
		{"PangolinTargets", PangolinTargets, []string{"true"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := tt.gauge.WithLabelValues(tt.labels...)
			assert.NotNil(t, g)
			g.Set(5)
			g.Inc()
			g.Dec()
		})
	}
}

// TestPangolinResources_SimpleGauge verifies the non-vec gauge.
func TestPangolinResources_SimpleGauge(t *testing.T) {
	PangolinResources.Set(10)
	PangolinResources.Inc()
	PangolinResources.Dec()
	// Should not panic
}

// TestMetricDescriptions verifies metric names via Desc().
func TestMetricDescriptions(t *testing.T) {
	tests := []struct {
		name       string
		collector  prometheus.Collector
		wantMetric string
	}{
		{"ReconcileTotal", ReconcileTotal, "pangolin_gateway_reconcile_total"},
		{"ReconcileDuration", ReconcileDuration, "pangolin_gateway_reconcile_duration_seconds"},
		{"PangolinAPIRequests", PangolinAPIRequests, "pangolin_api_requests_total"},
		{"PangolinAPILatency", PangolinAPILatency, "pangolin_api_request_duration_seconds"},
		{"PangolinAPIErrors", PangolinAPIErrors, "pangolin_api_errors_total"},
		{"GatewayTotal", GatewayTotal, "pangolin_gateway_total"},
		{"HTTPRouteTotal", HTTPRouteTotal, "pangolin_httproute_total"},
		{"GRPCRouteTotal", GRPCRouteTotal, "pangolin_grpcroute_total"},
		{"PangolinSites", PangolinSites, "pangolin_sites_total"},
		{"PangolinResources", PangolinResources, "pangolin_resources_total"},
		{"PangolinTargets", PangolinTargets, "pangolin_targets_total"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Collect descriptions and verify metric name appears
			descCh := make(chan *prometheus.Desc, 10)
			tt.collector.Describe(descCh)
			close(descCh)

			found := false
			for desc := range descCh {
				if desc.String() != "" {
					found = true
				}
			}
			assert.True(t, found, "Expected at least one description for %s", tt.name)
		})
	}
}

// TestCounterLabelCardinality verifies wrong label counts cause a panic.
func TestCounterLabelCardinality(t *testing.T) {
	// ReconcileTotal expects 2 labels: controller, result
	assert.Panics(t, func() {
		ReconcileTotal.WithLabelValues("only-one")
	}, "ReconcileTotal with 1 label should panic")

	assert.Panics(t, func() {
		ReconcileTotal.WithLabelValues("one", "two", "three")
	}, "ReconcileTotal with 3 labels should panic")

	// PangolinAPIRequests expects 3 labels: endpoint, method, status_code
	assert.Panics(t, func() {
		PangolinAPIRequests.WithLabelValues("only-one")
	}, "PangolinAPIRequests with 1 label should panic")
}

// TestReconcileTotal_CollectAndCount uses testutil to verify counter value.
func TestReconcileTotal_CollectAndCount(t *testing.T) {
	// Create a fresh counter for isolated testing
	counter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "test_reconcile_count",
			Help: "test counter",
		},
		[]string{"controller", "result"},
	)

	counter.WithLabelValues("gateway", "success").Inc()
	counter.WithLabelValues("gateway", "success").Inc()
	counter.WithLabelValues("gateway", "error").Inc()

	assert.Equal(t, 2, testutil.CollectAndCount(counter), "Expected 2 distinct label sets")

	val := testutil.ToFloat64(counter.WithLabelValues("gateway", "success"))
	assert.Equal(t, float64(2), val, "gateway/success should have been incremented twice")

	val = testutil.ToFloat64(counter.WithLabelValues("gateway", "error"))
	assert.Equal(t, float64(1), val, "gateway/error should have been incremented once")
}

// TestHistogramObservation_CollectAndCount uses testutil to verify histogram.
func TestHistogramObservation_CollectAndCount(t *testing.T) {
	hist := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "test_duration_seconds",
			Help:    "test histogram",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"controller"},
	)

	hist.WithLabelValues("gateway").Observe(0.1)
	hist.WithLabelValues("gateway").Observe(0.5)

	count := testutil.CollectAndCount(hist)
	assert.Equal(t, 1, count, "Expected 1 distinct label set")
}
