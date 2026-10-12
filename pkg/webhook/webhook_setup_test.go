package webhook_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/dxas90/pangolin-gateway-controller/pkg/webhook"
)

// TestSetupWebhookWithManager verifies the webhook wiring registers both
// validating admission handlers on a real controller-runtime manager without
// error. This exercises the production setup path used by cmd/controller/main.go.
func TestSetupWebhookWithManager(t *testing.T) {
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd")},
		ErrorIfCRDPathMissing: false,
	}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Skipf("envtest unavailable (%v); skipping webhook manager wiring test", err)
	}
	defer func() { _ = testEnv.Stop() }()

	require.NoError(t, gatewayv1.Install(scheme.Scheme))

	// Each manager owns one webhook server; registering the same path twice on
	// one server panics, so use a fresh manager per setup function under test.
	mgr1, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsserver.Options{BindAddress: "0"}, // disable metrics listener
	})
	require.NoError(t, err, "manager must be constructable from envtest config")

	// The function under test: registers /validate-gateway and /validate-httproute.
	require.NoError(t, webhook.SetupWebhookWithManager(mgr1, "pangolin"),
		"webhook registration must succeed")

	mgr2, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	require.NoError(t, err)

	// The scheme-override convenience wrapper must behave identically on a
	// fresh manager.
	require.NoError(t, webhook.SetupWebhookWithManagerAndScheme(mgr2, scheme.Scheme, "pangolin"),
		"scheme-override wrapper must succeed")
}
