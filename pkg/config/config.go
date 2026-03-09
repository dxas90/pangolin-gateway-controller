package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the controller configuration
type Config struct {
	// Pangolin API configuration
	Pangolin PangolinConfig `yaml:"pangolin"`

	// Kubernetes configuration
	Kubernetes KubernetesConfig `yaml:"kubernetes"`

	// Controller configuration
	Controller ControllerConfig `yaml:"controller"`

	// Logging configuration
	Logging LoggingConfig `yaml:"logging"`
}

// PangolinConfig contains Pangolin API settings
type PangolinConfig struct {
	// APIKey is the Pangolin API key for authentication
	APIKey string `yaml:"apiKey"`

	// OrgID is the Pangolin organization ID
	OrgID string `yaml:"orgId"`

	// BaseURL is the base URL for the Pangolin API
	BaseURL string `yaml:"baseUrl"`
}

// KubernetesConfig contains Kubernetes client settings
type KubernetesConfig struct {
	// Kubeconfig path (optional, defaults to in-cluster config)
	Kubeconfig string `yaml:"kubeconfig"`

	// Context to use from kubeconfig (optional)
	Context string `yaml:"context"`
}

// ControllerConfig contains controller runtime settings
type ControllerConfig struct {
	// GatewayClassName is the name of the GatewayClass this controller manages
	GatewayClassName string `yaml:"gatewayClassName"`

	// Namespace to watch (empty for all namespaces)
	Namespace string `yaml:"namespace"`

	// MetricsBindAddress is the address to bind the metrics server
	MetricsBindAddress string `yaml:"metricsBindAddress"`

	// HealthProbeBindAddress is the address to bind the health probe server
	HealthProbeBindAddress string `yaml:"healthProbeBindAddress"`

	// LeaderElection enables leader election
	LeaderElection bool `yaml:"leaderElection"`

	// LeaderElectionID is the name of the configmap used for leader election
	LeaderElectionID string `yaml:"leaderElectionId"`

	// LeaderElectionNamespace is the namespace for leader election
	LeaderElectionNamespace string `yaml:"leaderElectionNamespace"`

	// NewtImage is the Docker image for the newt VPN client
	NewtImage string `yaml:"newtImage"`

	// NewtEndpoint is the Pangolin endpoint for newt VPN authentication
	NewtEndpoint string `yaml:"newtEndpoint"`

	// MaxConcurrentReconciles is the maximum number of concurrent reconciliations per controller
	MaxConcurrentReconciles int `yaml:"maxConcurrentReconciles"`

	// RateLimiterBaseDelay is the minimum base delay for the exponential rate limiter
	RateLimiterBaseDelay time.Duration `yaml:"rateLimiterBaseDelay"`

	// RateLimiterMaxDelay is the maximum delay for the exponential rate limiter
	RateLimiterMaxDelay time.Duration `yaml:"rateLimiterMaxDelay"`

	// WorkqueueQPS is the token bucket QPS for the workqueue bucket rate limiter
	WorkqueueQPS float64 `yaml:"workqueueQPS"`

	// WorkqueueBurst is the burst size for the workqueue bucket rate limiter
	WorkqueueBurst int `yaml:"workqueueBurst"`
}

// LoggingConfig contains logging settings
type LoggingConfig struct {
	// Level is the log level (debug, info, warn, error)
	Level string `yaml:"level"`

	// Format is the log format (json, console)
	Format string `yaml:"format"`
}

// LoadConfig loads configuration from a file
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Apply defaults
	config.ApplyDefaults()

	// Validate configuration
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &config, nil
}

// ApplyDefaults sets default values for unspecified configuration
func (c *Config) ApplyDefaults() {
	if c.Pangolin.BaseURL == "" {
		c.Pangolin.BaseURL = "https://api.pangolin.net/v1"
	}

	if c.Controller.GatewayClassName == "" {
		c.Controller.GatewayClassName = "pangolin"
	}

	if c.Controller.MetricsBindAddress == "" {
		c.Controller.MetricsBindAddress = ":8080"
	}

	if c.Controller.HealthProbeBindAddress == "" {
		c.Controller.HealthProbeBindAddress = ":8081"
	}

	if c.Controller.LeaderElectionID == "" {
		c.Controller.LeaderElectionID = "pangolin-gateway-controller-leader"
	}

	if c.Controller.LeaderElectionNamespace == "" {
		c.Controller.LeaderElectionNamespace = "pangolin-system"
	}

	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}

	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}

	if c.Controller.NewtImage == "" {
		c.Controller.NewtImage = "docker.io/fosrl/newt:1.10.0"
	}

	if c.Controller.MaxConcurrentReconciles <= 0 {
		c.Controller.MaxConcurrentReconciles = 3
	}

	if c.Controller.RateLimiterBaseDelay <= 0 {
		c.Controller.RateLimiterBaseDelay = 5 * time.Millisecond
	}

	if c.Controller.RateLimiterMaxDelay <= 0 {
		c.Controller.RateLimiterMaxDelay = 1000 * time.Second
	}

	if c.Controller.WorkqueueQPS <= 0 {
		c.Controller.WorkqueueQPS = 10.0
	}

	if c.Controller.WorkqueueBurst <= 0 {
		c.Controller.WorkqueueBurst = 100
	}

	if c.Controller.NewtEndpoint == "" {
		// Derive newt endpoint from Pangolin API URL
		// If API is api.example.com, newt connects to example.com
		if c.Pangolin.BaseURL != "" {
			if parsed, err := url.Parse(c.Pangolin.BaseURL); err == nil {
				host := parsed.Hostname()
				// Remove "api." prefix if present
				if after, ok := strings.CutPrefix(host, "api."); ok {
					host = after
				}
				// Reconstruct URL with just scheme and host (no path)
				c.Controller.NewtEndpoint = fmt.Sprintf("%s://pangolin.%s", parsed.Scheme, host)
			} else {
				c.Controller.NewtEndpoint = "https://api.pangolin.net"
			}
		} else {
			c.Controller.NewtEndpoint = "https://api.pangolin.net"
		}
	}
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.Pangolin.APIKey == "" {
		return fmt.Errorf("pangolin.apiKey is required")
	}

	if c.Pangolin.OrgID == "" {
		return fmt.Errorf("pangolin.orgId is required")
	}

	if c.Controller.GatewayClassName == "" {
		return fmt.Errorf("controller.gatewayClassName is required")
	}

	// Validate log level
	validLevels := map[string]bool{
		"debug": true,
		"info":  true,
		"warn":  true,
		"error": true,
	}

	if !validLevels[c.Logging.Level] {
		return fmt.Errorf("invalid logging.level: %s (must be debug, info, warn, or error)", c.Logging.Level)
	}

	return nil
}

// LoadFromEnv loads configuration from environment variables
func LoadFromEnv() (*Config, error) {
	controllerCfg := ControllerConfig{
		GatewayClassName:        os.Getenv("GATEWAY_CLASS_NAME"),
		Namespace:               os.Getenv("WATCH_NAMESPACE"),
		MetricsBindAddress:      os.Getenv("METRICS_BIND_ADDRESS"),
		HealthProbeBindAddress:  os.Getenv("HEALTH_PROBE_BIND_ADDRESS"),
		LeaderElection:          os.Getenv("ENABLE_LEADER_ELECTION") == "true",
		LeaderElectionID:        os.Getenv("LEADER_ELECTION_ID"),
		LeaderElectionNamespace: os.Getenv("LEADER_ELECTION_NAMESPACE"),
		NewtImage:               os.Getenv("NEWT_IMAGE"),
		NewtEndpoint:            os.Getenv("NEWT_ENDPOINT"),
	}

	if v := os.Getenv("MAX_CONCURRENT_RECONCILES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			controllerCfg.MaxConcurrentReconciles = n
		}
	}

	if v := os.Getenv("RATE_LIMITER_BASE_DELAY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			controllerCfg.RateLimiterBaseDelay = d
		}
	}

	if v := os.Getenv("RATE_LIMITER_MAX_DELAY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			controllerCfg.RateLimiterMaxDelay = d
		}
	}

	if v := os.Getenv("WORKQUEUE_QPS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			controllerCfg.WorkqueueQPS = f
		}
	}

	if v := os.Getenv("WORKQUEUE_BURST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			controllerCfg.WorkqueueBurst = n
		}
	}

	config := &Config{
		Pangolin: PangolinConfig{
			APIKey:  os.Getenv("PANGOLIN_API_KEY"),
			OrgID:   os.Getenv("PANGOLIN_ORG_ID"),
			BaseURL: os.Getenv("PANGOLIN_BASE_URL"),
		},
		Controller: controllerCfg,
		Kubernetes: KubernetesConfig{
			Kubeconfig: os.Getenv("KUBECONFIG"),
			Context:    os.Getenv("KUBE_CONTEXT"),
		},
		Logging: LoggingConfig{
			Level:  os.Getenv("LOG_LEVEL"),
			Format: os.Getenv("LOG_FORMAT"),
		},
	}

	// Apply defaults
	config.ApplyDefaults()

	// Validate
	if err := config.Validate(); err != nil {
		return nil, err
	}

	return config, nil
}
