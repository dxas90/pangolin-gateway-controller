package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	testAPIKey = "test-api-key"
	testOrgID  = "test-org-id"
)

// validConfig returns a minimal Config that passes Validate().
func validConfig() *Config {
	return &Config{
		Pangolin: PangolinConfig{
			APIKey:  testAPIKey,
			OrgID:   testOrgID,
			BaseURL: "https://api.example.com/v1",
		},
		Controller: ControllerConfig{
			GatewayClassName: "pangolin",
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "json",
		},
	}
}

// ---------------------------------------------------------------------------
// TestApplyDefaults
// ---------------------------------------------------------------------------

func TestApplyDefaults_SetsAllEmptyFields(t *testing.T) {
	c := &Config{
		Pangolin: PangolinConfig{
			APIKey: testAPIKey,
			OrgID:  testOrgID,
		},
	}
	c.ApplyDefaults()

	assert.Equal(t, "https://api.pangolin.net/v1", c.Pangolin.BaseURL)
	assert.Equal(t, "pangolin", c.Controller.GatewayClassName)
	assert.Equal(t, ":8080", c.Controller.MetricsBindAddress)
	assert.Equal(t, ":8081", c.Controller.HealthProbeBindAddress)
	assert.Equal(t, "pangolin-gateway-controller-leader", c.Controller.LeaderElectionID)
	assert.Equal(t, "pangolin-system", c.Controller.LeaderElectionNamespace)
	assert.Equal(t, "docker.io/fosrl/newt:1.10.0", c.Controller.NewtImage)
	assert.Equal(t, 3, c.Controller.MaxConcurrentReconciles)
	assert.Equal(t, 5*time.Millisecond, c.Controller.RateLimiterBaseDelay)
	assert.Equal(t, 1000*time.Second, c.Controller.RateLimiterMaxDelay)
	assert.Equal(t, 10.0, c.Controller.WorkqueueQPS)
	assert.Equal(t, 100, c.Controller.WorkqueueBurst)
	assert.Equal(t, "info", c.Logging.Level)
	assert.Equal(t, "json", c.Logging.Format)
}

func TestApplyDefaults_DoesNotOverwriteExistingValues(t *testing.T) {
	c := &Config{
		Pangolin: PangolinConfig{
			BaseURL: "https://custom.api.com/v2",
		},
		Controller: ControllerConfig{
			GatewayClassName:        "custom-class",
			MetricsBindAddress:      ":9090",
			HealthProbeBindAddress:  ":9091",
			LeaderElectionID:        "custom-leader",
			LeaderElectionNamespace: "custom-ns",
			NewtImage:               "custom/newt:2.0.0",
			NewtEndpoint:            "https://custom.endpoint.com",
			MaxConcurrentReconciles: 5,
			RateLimiterBaseDelay:    10 * time.Millisecond,
			RateLimiterMaxDelay:     500 * time.Second,
			WorkqueueQPS:            20.0,
			WorkqueueBurst:          200,
		},
		Logging: LoggingConfig{
			Level:  "debug",
			Format: "console",
		},
	}
	c.ApplyDefaults()

	assert.Equal(t, "https://custom.api.com/v2", c.Pangolin.BaseURL)
	assert.Equal(t, "custom-class", c.Controller.GatewayClassName)
	assert.Equal(t, ":9090", c.Controller.MetricsBindAddress)
	assert.Equal(t, ":9091", c.Controller.HealthProbeBindAddress)
	assert.Equal(t, "custom-leader", c.Controller.LeaderElectionID)
	assert.Equal(t, "custom-ns", c.Controller.LeaderElectionNamespace)
	assert.Equal(t, "custom/newt:2.0.0", c.Controller.NewtImage)
	assert.Equal(t, "https://custom.endpoint.com", c.Controller.NewtEndpoint)
	assert.Equal(t, 5, c.Controller.MaxConcurrentReconciles)
	assert.Equal(t, 10*time.Millisecond, c.Controller.RateLimiterBaseDelay)
	assert.Equal(t, 500*time.Second, c.Controller.RateLimiterMaxDelay)
	assert.Equal(t, 20.0, c.Controller.WorkqueueQPS)
	assert.Equal(t, 200, c.Controller.WorkqueueBurst)
	assert.Equal(t, "debug", c.Logging.Level)
	assert.Equal(t, "console", c.Logging.Format)
}

func TestApplyDefaults_NewtEndpointDerivedFromBaseURL(t *testing.T) {
	tests := []struct {
		name             string
		baseURL          string
		expectedEndpoint string
	}{
		{
			name:             "strips api prefix and adds pangolin prefix",
			baseURL:          "https://api.example.com/v1",
			expectedEndpoint: "https://pangolin.example.com",
		},
		{
			name:             "no api prefix keeps host and adds pangolin prefix",
			baseURL:          "https://custom.example.com/v1",
			expectedEndpoint: "https://pangolin.custom.example.com",
		},
		{
			name:             "http scheme is preserved",
			baseURL:          "http://api.local.dev/v1",
			expectedEndpoint: "http://pangolin.local.dev",
		},
		{
			name:             "empty base URL falls back to default",
			baseURL:          "",
			expectedEndpoint: "https://pangolin.pangolin.net",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{
				Pangolin: PangolinConfig{BaseURL: tt.baseURL},
			}
			c.ApplyDefaults()
			assert.Equal(t, tt.expectedEndpoint, c.Controller.NewtEndpoint)
		})
	}
}

func TestApplyDefaults_NewtEndpointFallbackOnInvalidURL(t *testing.T) {
	c := &Config{
		Pangolin: PangolinConfig{BaseURL: "://invalid-url"},
	}
	c.ApplyDefaults()
	assert.Equal(t, "https://api.pangolin.net", c.Controller.NewtEndpoint)
}

// ---------------------------------------------------------------------------
// TestValidate
// ---------------------------------------------------------------------------

func TestValidate_ValidConfig(t *testing.T) {
	c := validConfig()
	assert.NoError(t, c.Validate())
}

func TestValidate_MissingRequiredFields(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*Config)
		errContains string
	}{
		{
			name:        "missing API key",
			mutate:      func(c *Config) { c.Pangolin.APIKey = "" },
			errContains: "pangolin.apiKey is required",
		},
		{
			name:        "missing org ID",
			mutate:      func(c *Config) { c.Pangolin.OrgID = "" },
			errContains: "pangolin.orgId is required",
		},
		{
			name:        "missing gateway class name",
			mutate:      func(c *Config) { c.Controller.GatewayClassName = "" },
			errContains: "controller.gatewayClassName is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.mutate(c)
			err := c.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

func TestValidate_InvalidLogLevel(t *testing.T) {
	c := validConfig()
	c.Logging.Level = "verbose"
	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid logging.level")
}

func TestValidate_AllLogLevels(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		t.Run(level, func(t *testing.T) {
			c := validConfig()
			c.Logging.Level = level
			assert.NoError(t, c.Validate())
		})
	}
}

func TestValidate_BaseDelayExceedsMaxDelay(t *testing.T) {
	c := validConfig()
	c.Logging.Level = "info"
	c.Controller.RateLimiterBaseDelay = 10 * time.Second
	c.Controller.RateLimiterMaxDelay = 1 * time.Second // less than base delay
	err := c.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "rateLimiterBaseDelay")
}

func TestValidate_BaseDelayEqualsMaxDelay(t *testing.T) {
	c := validConfig()
	c.Logging.Level = "info"
	c.Controller.RateLimiterBaseDelay = 5 * time.Second
	c.Controller.RateLimiterMaxDelay = 5 * time.Second // equal is allowed
	err := c.Validate()
	require.NoError(t, err)
}

func TestValidate_NegativeBaseDelay(t *testing.T) {
	c := validConfig()
	c.Logging.Level = "info"
	c.Controller.RateLimiterBaseDelay = -1 * time.Millisecond
	err := c.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "rateLimiterBaseDelay")
}

// ---------------------------------------------------------------------------
// TestLoadFromEnv
// ---------------------------------------------------------------------------

func TestLoadFromEnv_AllEnvVars(t *testing.T) {
	t.Setenv("PANGOLIN_API_KEY", testAPIKey)
	t.Setenv("PANGOLIN_ORG_ID", testOrgID)
	t.Setenv("PANGOLIN_BASE_URL", "https://api.custom.com/v1")
	t.Setenv("GATEWAY_CLASS_NAME", "my-class")
	t.Setenv("WATCH_NAMESPACE", "my-ns")
	t.Setenv("METRICS_BIND_ADDRESS", ":9090")
	t.Setenv("HEALTH_PROBE_BIND_ADDRESS", ":9091")
	t.Setenv("ENABLE_LEADER_ELECTION", "true")
	t.Setenv("LEADER_ELECTION_ID", "my-leader")
	t.Setenv("LEADER_ELECTION_NAMESPACE", "my-leader-ns")
	t.Setenv("NEWT_IMAGE", "custom/newt:2.0")
	t.Setenv("NEWT_ENDPOINT", "https://pangolin.custom.com")
	t.Setenv("MAX_CONCURRENT_RECONCILES", "10")
	t.Setenv("RATE_LIMITER_BASE_DELAY", "100ms")
	t.Setenv("RATE_LIMITER_MAX_DELAY", "30s")
	t.Setenv("WORKQUEUE_QPS", "25.5")
	t.Setenv("WORKQUEUE_BURST", "50")
	t.Setenv("KUBECONFIG", "/tmp/kubeconfig")
	t.Setenv("KUBE_CONTEXT", "my-context")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_FORMAT", "console")

	cfg, err := LoadFromEnv()
	require.NoError(t, err)

	assert.Equal(t, testAPIKey, cfg.Pangolin.APIKey)
	assert.Equal(t, testOrgID, cfg.Pangolin.OrgID)
	assert.Equal(t, "https://api.custom.com/v1", cfg.Pangolin.BaseURL)
	assert.Equal(t, "my-class", cfg.Controller.GatewayClassName)
	assert.Equal(t, "my-ns", cfg.Controller.Namespace)
	assert.Equal(t, ":9090", cfg.Controller.MetricsBindAddress)
	assert.Equal(t, ":9091", cfg.Controller.HealthProbeBindAddress)
	assert.True(t, cfg.Controller.LeaderElection)
	assert.Equal(t, "my-leader", cfg.Controller.LeaderElectionID)
	assert.Equal(t, "my-leader-ns", cfg.Controller.LeaderElectionNamespace)
	assert.Equal(t, "custom/newt:2.0", cfg.Controller.NewtImage)
	assert.Equal(t, "https://pangolin.custom.com", cfg.Controller.NewtEndpoint)
	assert.Equal(t, 10, cfg.Controller.MaxConcurrentReconciles)
	assert.Equal(t, 100*time.Millisecond, cfg.Controller.RateLimiterBaseDelay)
	assert.Equal(t, 30*time.Second, cfg.Controller.RateLimiterMaxDelay)
	assert.Equal(t, 25.5, cfg.Controller.WorkqueueQPS)
	assert.Equal(t, 50, cfg.Controller.WorkqueueBurst)
	assert.Equal(t, "/tmp/kubeconfig", cfg.Kubernetes.Kubeconfig)
	assert.Equal(t, "my-context", cfg.Kubernetes.Context)
	assert.Equal(t, "debug", cfg.Logging.Level)
	assert.Equal(t, "console", cfg.Logging.Format)
}

func TestLoadFromEnv_DefaultsAppliedWhenEnvVarsUnset(t *testing.T) {
	// Only set the required env vars
	t.Setenv("PANGOLIN_API_KEY", testAPIKey)
	t.Setenv("PANGOLIN_ORG_ID", testOrgID)

	cfg, err := LoadFromEnv()
	require.NoError(t, err)

	assert.Equal(t, "https://api.pangolin.net/v1", cfg.Pangolin.BaseURL)
	assert.Equal(t, "pangolin", cfg.Controller.GatewayClassName)
	assert.Equal(t, ":8080", cfg.Controller.MetricsBindAddress)
	assert.Equal(t, ":8081", cfg.Controller.HealthProbeBindAddress)
	assert.Equal(t, "docker.io/fosrl/newt:1.10.0", cfg.Controller.NewtImage)
	assert.Equal(t, 3, cfg.Controller.MaxConcurrentReconciles)
	assert.Equal(t, "info", cfg.Logging.Level)
	assert.Equal(t, "json", cfg.Logging.Format)
}

func TestLoadFromEnv_LeaderElectionFalse(t *testing.T) {
	t.Setenv("PANGOLIN_API_KEY", testAPIKey)
	t.Setenv("PANGOLIN_ORG_ID", testOrgID)
	t.Setenv("ENABLE_LEADER_ELECTION", "false")

	cfg, err := LoadFromEnv()
	require.NoError(t, err)
	assert.False(t, cfg.Controller.LeaderElection)
}

func TestLoadFromEnv_LeaderElectionNotSetDefaultsFalse(t *testing.T) {
	t.Setenv("PANGOLIN_API_KEY", testAPIKey)
	t.Setenv("PANGOLIN_ORG_ID", testOrgID)

	cfg, err := LoadFromEnv()
	require.NoError(t, err)
	assert.False(t, cfg.Controller.LeaderElection)
}

func TestLoadFromEnv_MissingRequiredVarsFails(t *testing.T) {
	// No env vars set → validation must fail
	_, err := LoadFromEnv()
	require.Error(t, err)
}

func TestLoadFromEnv_InvalidNumericEnvVarsIgnored(t *testing.T) {
	t.Setenv("PANGOLIN_API_KEY", testAPIKey)
	t.Setenv("PANGOLIN_ORG_ID", testOrgID)
	t.Setenv("MAX_CONCURRENT_RECONCILES", "not-a-number")
	t.Setenv("WORKQUEUE_QPS", "abc")
	t.Setenv("WORKQUEUE_BURST", "xyz")
	t.Setenv("RATE_LIMITER_BASE_DELAY", "invalid")
	t.Setenv("RATE_LIMITER_MAX_DELAY", "invalid")

	cfg, err := LoadFromEnv()
	require.NoError(t, err)

	// Defaults should be applied since parsing failed silently
	assert.Equal(t, 3, cfg.Controller.MaxConcurrentReconciles)
	assert.Equal(t, 10.0, cfg.Controller.WorkqueueQPS)
	assert.Equal(t, 100, cfg.Controller.WorkqueueBurst)
	assert.Equal(t, 5*time.Millisecond, cfg.Controller.RateLimiterBaseDelay)
	assert.Equal(t, 1000*time.Second, cfg.Controller.RateLimiterMaxDelay)
}

// ---------------------------------------------------------------------------
// TestLoadConfig (file-based)
// ---------------------------------------------------------------------------

func TestLoadConfig_ValidFile(t *testing.T) {
	content := `
pangolin:
  apiKey: "file-api-key"
  orgId: "file-org-id"
  baseUrl: "https://api.file.com/v1"
controller:
  gatewayClassName: "file-class"
  newtImage: "file/newt:3.0"
logging:
  level: "warn"
  format: "console"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)

	assert.Equal(t, "file-api-key", cfg.Pangolin.APIKey)
	assert.Equal(t, "file-org-id", cfg.Pangolin.OrgID)
	assert.Equal(t, "https://api.file.com/v1", cfg.Pangolin.BaseURL)
	assert.Equal(t, "file-class", cfg.Controller.GatewayClassName)
	assert.Equal(t, "file/newt:3.0", cfg.Controller.NewtImage)
	assert.Equal(t, "warn", cfg.Logging.Level)
	assert.Equal(t, "console", cfg.Logging.Format)
	// Defaults applied for unset fields
	assert.Equal(t, ":8080", cfg.Controller.MetricsBindAddress)
	assert.Equal(t, ":8081", cfg.Controller.HealthProbeBindAddress)
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path/config.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read config file")
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("{{invalid yaml"), 0600))

	_, err := LoadConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse config file")
}

func TestLoadConfig_MissingRequiredFieldsFails(t *testing.T) {
	content := `
pangolin:
  baseUrl: "https://api.example.com/v1"
logging:
  level: "info"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))

	_, err := LoadConfig(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid configuration")
}

func TestLoadConfig_DefaultsApplied(t *testing.T) {
	content := `
pangolin:
  apiKey: "key"
  orgId: "org"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)

	// Defaults should have been applied before validation
	assert.Equal(t, "pangolin", cfg.Controller.GatewayClassName)
	assert.Equal(t, "https://api.pangolin.net/v1", cfg.Pangolin.BaseURL)
	assert.Equal(t, "info", cfg.Logging.Level)
}
