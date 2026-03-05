// Package testutil provides testing utilities and helpers for controller tests.
// It sets up envtest with a real etcd and API server for integration testing.
package testutil

import (
	"context"
	"path/filepath"
	"time"

	"github.com/stretchr/testify/suite"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// EnvTestSuite provides a base test suite with envtest setup.
// It spins up a real etcd and API server for integration testing.
type EnvTestSuite struct {
	suite.Suite
	cfg       *rest.Config
	k8sClient client.Client
	testEnv   *envtest.Environment
	ctx       context.Context
	cancel    context.CancelFunc
}

// SetupSuite sets up the envtest environment before all tests.
// This runs once per test suite.
func (s *EnvTestSuite) SetupSuite() {
	s.ctx, s.cancel = context.WithCancel(context.Background())

	// Add Gateway API scheme
	err := gatewayv1.Install(scheme.Scheme)
	s.Require().NoError(err, "failed to add Gateway API scheme")

	// Setup envtest with Gateway API CRDs
	s.testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd"),
			// Gateway API CRDs will be installed from the module
		},
		ErrorIfCRDPathMissing: false,
	}

	// Start the test environment
	cfg, err := s.testEnv.Start()
	s.Require().NoError(err, "failed to start test environment")
	s.Require().NotNil(cfg, "config should not be nil")

	s.cfg = cfg

	// Create a client for testing
	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	s.Require().NoError(err, "failed to create client")
	s.k8sClient = k8sClient
}

// TearDownSuite tears down the envtest environment after all tests.
func (s *EnvTestSuite) TearDownSuite() {
	s.cancel()
	err := s.testEnv.Stop()
	s.Require().NoError(err, "failed to stop test environment")
}

// Client returns the Kubernetes client for testing.
func (s *EnvTestSuite) Client() client.Client {
	return s.k8sClient
}

// Config returns the rest.Config for the test environment.
func (s *EnvTestSuite) Config() *rest.Config {
	return s.cfg
}

// Context returns the test context.
func (s *EnvTestSuite) Context() context.Context {
	return s.ctx
}

// WaitForCacheSync waits for the manager's cache to sync.
// Use this after starting a controller manager in tests.
func (s *EnvTestSuite) WaitForCacheSync(mgr ctrl.Manager) {
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()

	synced := mgr.GetCache().WaitForCacheSync(ctx)
	s.Require().True(synced, "cache failed to sync within timeout")
}

// Eventually wraps testify's Eventually with default timeout and interval.
// Use this instead of time.Sleep() in tests.
func (s *EnvTestSuite) Eventually(condition func() bool, msgAndArgs ...interface{}) {
	s.Require().Eventually(
		condition,
		10*time.Second,       // timeout
		100*time.Millisecond, // poll interval
		msgAndArgs...,
	)
}

// EventuallyWithTimeout provides custom timeout for Eventually.
func (s *EnvTestSuite) EventuallyWithTimeout(condition func() bool, timeout time.Duration, msgAndArgs ...interface{}) {
	s.Require().Eventually(
		condition,
		timeout,
		100*time.Millisecond,
		msgAndArgs...,
	)
}
