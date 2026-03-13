package main

import (
	"flag"
	"os"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/config"
	pgctrl "github.com/dxas90/pangolin-gateway-controller/pkg/controller"
	pgmetrics "github.com/dxas90/pangolin-gateway-controller/pkg/metrics"
	_ "github.com/dxas90/pangolin-gateway-controller/pkg/metrics"
	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/dxas90/pangolin-gateway-controller/pkg/webhook"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// version and buildDate are injected at build time via -ldflags.
var (
	version   = "dev"
	buildDate = "unknown"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(gatewayv1.Install(scheme))
}

func main() {
	var configFile string
	var useEnvConfig bool

	flag.StringVar(&configFile, "config", "", "Path to configuration file")
	flag.BoolVar(&useEnvConfig, "env-config", false, "Load configuration from environment variables")
	flag.Parse()

	// Load configuration
	var cfg *config.Config
	var err error

	if useEnvConfig || configFile == "" {
		setupLog.Info("Loading configuration from environment variables")
		cfg, err = config.LoadFromEnv()
	} else {
		setupLog.Info("Loading configuration from file", "path", configFile)
		cfg, err = config.LoadConfig(configFile)
	}

	if err != nil {
		setupLog.Error(err, "Failed to load configuration")
		os.Exit(1)
	}

	// Setup logging
	opts := zap.Options{
		Development: cfg.Logging.Level == "debug",
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	setupLog.Info("Starting Pangolin Gateway Controller",
		"version", version,
		"buildDate", buildDate,
		"gatewayClass", cfg.Controller.GatewayClassName,
	)

	// Create Pangolin client
	pangolinClient := pangolin.NewClient(cfg.Pangolin.APIKey, cfg.Pangolin.OrgID)
	if cfg.Pangolin.BaseURL != "" {
		pangolinClient.BaseURL = cfg.Pangolin.BaseURL
	}

	// Wire circuit breaker state-change logging and metrics
	pangolinClient.Breaker.SetStateChangeCallback(func(from, to string) {
		setupLog.Info("Pangolin API circuit breaker state changed", "from", from, "to", to)
		switch to {
		case "closed":
			pgmetrics.CircuitBreakerState.Set(0)
		case "open":
			pgmetrics.CircuitBreakerState.Set(1)
		case "half-open":
			pgmetrics.CircuitBreakerState.Set(2)
		}
	})

	// Setup manager
	// SyncPeriod forces a full re-list of all watched objects at this interval,
	// ensuring Pangolin-side drift (deleted resources/targets/sites) is detected
	// and corrected even when no Kubernetes events are firing.
	syncPeriod := 10 * time.Minute
	mgrOpts := ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 server.Options{BindAddress: cfg.Controller.MetricsBindAddress},
		HealthProbeBindAddress:  cfg.Controller.HealthProbeBindAddress,
		LeaderElection:          cfg.Controller.LeaderElection,
		LeaderElectionID:        cfg.Controller.LeaderElectionID,
		LeaderElectionNamespace: cfg.Controller.LeaderElectionNamespace,
		Cache: cache.Options{
			SyncPeriod: &syncPeriod,
		},
	}

	// Configure namespace watching if specified (preserve SyncPeriod)
	if cfg.Controller.Namespace != "" {
		mgrOpts.Cache.DefaultNamespaces = map[string]cache.Config{
			cfg.Controller.Namespace: {},
		}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), mgrOpts)
	if err != nil {
		setupLog.Error(err, "Failed to create manager")
		os.Exit(1)
	}

	// Setup field indexes for efficient querying (O(1) lookups instead of O(n) scans)
	if err := pgctrl.SetupIndexes(mgr); err != nil {
		setupLog.Error(err, "Failed to setup field indexes")
		os.Exit(1)
	}

	// Setup Gateway controller
	if err = (&pgctrl.GatewayReconciler{
		Client:          mgr.GetClient(),
		Log:             ctrl.Log.WithName("controllers").WithName("Gateway"),
		Scheme:          mgr.GetScheme(),
		PangolinClient:  pangolinClient,
		ControllerClass: cfg.Controller.GatewayClassName,
		NewtEndpoint:    cfg.Controller.NewtEndpoint,
		Config:          &cfg.Controller,
		Recorder:        mgr.GetEventRecorderFor("gateway-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create Gateway controller")
		os.Exit(1)
	}

	// Setup GatewayClass controller
	if err = (&pgctrl.GatewayClassReconciler{
		Client:         mgr.GetClient(),
		Log:            ctrl.Log.WithName("controllers").WithName("GatewayClass"),
		Scheme:         mgr.GetScheme(),
		ControllerName: "pangol.in/gateway-controller",
		Config:         &cfg.Controller,
		Recorder:       mgr.GetEventRecorderFor("gatewayclass-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create GatewayClass controller")
		os.Exit(1)
	}

	// Setup HTTPRoute controller
	if err = (&pgctrl.HTTPRouteReconciler{
		Client:         mgr.GetClient(),
		Log:            ctrl.Log.WithName("controllers").WithName("HTTPRoute"),
		Scheme:         mgr.GetScheme(),
		PangolinClient: pangolinClient,
		Config:         &cfg.Controller,
		Recorder:       mgr.GetEventRecorderFor("httproute-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create HTTPRoute controller")
		os.Exit(1)
	}

	// Setup GRPCRoute controller for TCP/UDP services
	if err = (&pgctrl.GRPCRouteReconciler{
		Client:         mgr.GetClient(),
		Log:            ctrl.Log.WithName("controllers").WithName("GRPCRoute"),
		Scheme:         mgr.GetScheme(),
		PangolinClient: pangolinClient,
		Config:         &cfg.Controller,
		Recorder:       mgr.GetEventRecorderFor("grpcroute-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create GRPCRoute controller")
		os.Exit(1)
	}

	// Setup Newt controller to deploy newt instances for Gateways
	if err = (&pgctrl.NewtReconciler{
		Client:          mgr.GetClient(),
		Log:             ctrl.Log.WithName("controllers").WithName("Newt"),
		Scheme:          mgr.GetScheme(),
		PangolinClient:  pangolinClient,
		PangolinBaseURL: cfg.Pangolin.BaseURL,
		NewtEndpoint:    cfg.Controller.NewtEndpoint,
		NewtImage:       cfg.Controller.NewtImage,
		ControllerClass: cfg.Controller.GatewayClassName,
		Config:          &cfg.Controller,
		Recorder:        mgr.GetEventRecorderFor("newt-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create Newt controller")
		os.Exit(1)
	}

	// Setup admission webhooks (opt-in via ENABLE_WEBHOOKS=true)
	if os.Getenv("ENABLE_WEBHOOKS") == "true" {
		setupLog.Info("Setting up admission webhooks")
		if err := webhook.SetupWebhookWithManager(mgr, cfg.Controller.GatewayClassName); err != nil {
			setupLog.Error(err, "Failed to setup webhooks")
			os.Exit(1)
		}
	}

	// Setup health checks
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}

	if err := mgr.AddReadyzCheck("readyz", pgctrl.NewPangolinReadyChecker(pangolinClient)); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	// Start the manager
	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}
