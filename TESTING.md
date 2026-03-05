# Testing Guide

## Overview

This project uses **envtest** from controller-runtime for integration testing. Envtest spins up a real etcd and Kubernetes API server for testing controllers in a realistic environment.

## Quick Start

```bash
# Run all tests
make test

# Run tests with coverage
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out

# Run specific test suite
go test ./pkg/controller -run TestGatewayControllerSuite -v

# Run single test
go test ./pkg/controller -run TestGatewayControllerSuite/TestReconcile_NewGateway -v
```

## Test Structure

### Test Organization

```
pkg/
├── controller/
│   ├── gateway_controller.go
│   ├── gateway_controller_test.go      # Integration tests
│   ├── httproute_controller.go
│   └── httproute_controller_test.go
├── pangolin/
│   ├── client.go
│   └── client_test.go                  # Unit tests
└── testutil/
    ├── envtest.go                      # Envtest base suite
    ├── fixtures.go                     # Test data builders
    └── mock_pangolin.go                # Mock Pangolin client
```

### Test Types

#### 1. Unit Tests
- Test individual functions in isolation
- Use mocks for external dependencies
- Fast execution (<1s)
- Example: `pkg/pangolin/client_test.go`

#### 2. Integration Tests (Envtest)
- Test full reconciliation loops
- Real Kubernetes API server
- Mocked Pangolin API
- Moderate execution time (5-10s per suite)
- Example: `pkg/controller/gateway_controller_test.go`

## Envtest Setup

### Prerequisites

```bash
# Install envtest binaries (one-time setup)
make envtest

# Or manually:
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
setup-envtest use 1.28.x!
```

### Test Suite Pattern

All controller tests inherit from `testutil.EnvTestSuite`:

```go
type GatewayControllerTestSuite struct {
    testutil.EnvTestSuite  // Provides envtest setup
    reconciler    *controller.GatewayReconciler
    mockPangolin  *testutil.MockPangolinClient
}

func (s *GatewayControllerTestSuite) SetupTest() {
    // Create fresh mocks for each test
    s.mockPangolin = testutil.NewMockPangolinClient()

    // Create reconciler with mocks
    s.reconciler = &controller.GatewayReconciler{
        Client:         s.Client(),
        PangolinClient: s.mockPangolin,
        ...
    }
}

func (s *GatewayControllerTestSuite) TearDownTest() {
    // Verify all mock expectations were met
    s.mockPangolin.AssertExpectations(s.T())
}
```

## Writing Tests

### Test Structure (REQUIRED)

Follow this pattern for all integration tests:

```go
func (s *MySuite) TestMyFeature() {
    // 1. Setup: Create Kubernetes resources
    gateway := testutil.NewTestGateway("test", "default")
    err := s.Client().Create(s.Context(), gateway)
    s.Require().NoError(err)

    // 2. Mock: Setup Pangolin API expectations
    s.mockPangolin.On("CreateSite", s.Context(), mock.Anything).
        Return(&pangolin.Site{ID: 123}, nil).
        Once()

    // 3. Execute: Run reconciliation
    result, err := s.reconciler.Reconcile(s.Context(), req)
    s.Require().NoError(err)

    // 4. Verify: Check Kubernetes state asynchronously
    s.Eventually(func() bool {
        fresh := &gatewayv1.Gateway{}
        _ = s.Client().Get(s.Context(), key, fresh)
        return fresh.Labels["site-id"] == "123"
    }, "Gateway should have site ID label")

    // TearDownTest() will verify mock expectations
}
```

### Using Eventually (CRITICAL)

**NEVER use `time.Sleep()` in tests**. Always use `Eventually()`:

```go
// ❌ FORBIDDEN - Hardcoded sleeps are flaky
time.Sleep(500 * time.Millisecond)
fresh := &gatewayv1.Gateway{}
_ = s.Client().Get(ctx, key, fresh)
s.Require().Equal("123", fresh.Labels["site-id"])

// ✅ CORRECT - Eventually waits for condition
s.Eventually(func() bool {
    fresh := &gatewayv1.Gateway{}
    err := s.Client().Get(s.Context(), key, fresh)
    if err != nil {
        return false
    }
    return fresh.Labels["site-id"] == "123"
}, "Gateway should have site ID label")
```

**Why?**
- Envtest has caching - resources may not be immediately visible
- Status updates happen asynchronously
- Eventually retries until condition is true or timeout

### Mock Setup Patterns

#### Happy Path
```go
s.mockPangolin.On("CreateSite", s.Context(), mock.Anything).
    Return(&pangolin.Site{ID: 123}, nil).
    Once()  // Expect exactly one call
```

#### Error Cases
```go
s.mockPangolin.On("CreateSite", s.Context(), mock.Anything).
    Return(nil, errors.New("API error")).
    Once()
```

#### Conditional Mocks
```go
// May be called but not required
s.mockPangolin.On("DeleteSite", s.Context(), 123).
    Return(nil).
    Maybe()
```

## Test Data Builders

Use fixtures from `testutil` package for consistent test data:

```go
// Create test resources
gateway := testutil.NewTestGateway("my-gateway", "default")
route := testutil.NewTestHTTPRoute("my-route", "default", "my-gateway", "test.example.com")
service := testutil.NewTestService("backend", "default")

// Create in cluster
err := s.Client().Create(s.Context(), gateway)
s.Require().NoError(err)
```

## Test Constants

Always use constants from `testutil` package:

```go
const (
    TestNamespace     = "test-namespace"
    TestGatewayName   = "test-gateway"
    TestHTTPRouteName = "test-httproute"
    TestGatewayClass  = "pangolin"
    TestHostname      = "test.example.com"
)
```

## Coverage Goals

Target **70%+ code coverage** for:
- Controller reconciliation logic
- Pangolin client operations
- Error handling paths
- Status updates

```bash
# Check coverage
go test ./... -coverprofile=coverage.out
go tool cover -func=coverage.out | grep total

# View coverage HTML
go tool cover -html=coverage.out
```

## Common Test Scenarios

### Test Cases to Cover

For each controller, test:

1. ✅ **Happy path** - Resource created successfully
2. ✅ **Existing resource** - Idempotent reconciliation
3. ✅ **Deletion** - Finalizer cleanup
4. ✅ **Wrong class** - Skip resources with different class
5. ❌ **API errors** - Pangolin API returns errors
6. ❌ **Conflict errors** - Concurrent updates
7. ❌ **Missing dependencies** - Service not found
8. ❌ **Invalid configuration** - Malformed data

## Troubleshooting

### Tests Timeout

**Symptom:** Tests hang or timeout after 30s

**Solutions:**
```bash
# Increase test timeout
go test ./... -timeout=5m

# Check for missing Eventually
# Every assertion on async state should use Eventually()

# Verify envtest is installed
setup-envtest list
```

### Cache Not Syncing

**Symptom:** Resources created but not found in tests

**Solution:**
```go
// Wait for cache to sync after creating resources
s.Eventually(func() bool {
    var obj MyResource
    return s.Client().Get(ctx, key, &obj) == nil
}, "Resource should appear in cache")
```

### Mock Expectations Not Met

**Symptom:** `TearDownTest` fails with "Expected call not made"

**Solutions:**
```go
// Option 1: Use Maybe() for conditional calls
s.mockPangolin.On("DeleteSite", ...).Return(nil).Maybe()

// Option 2: Verify the code path actually calls the mock
// Add debug logging to see if method is reached

// Option 3: Check test logic - did test take different path?
```

### Flaky Tests

**Common Causes:**
1. Missing `Eventually()` - using direct assertions on async state
2. Race conditions - multiple goroutines modifying state
3. Stale cache - not waiting for cache sync
4. Resource conflicts - tests interfering with each other

**Solution:** Use `Eventually()` for ALL assertions on Kubernetes resources

## CI/CD Integration

### GitHub Actions

```yaml
# .github/workflows/test.yml
name: Test
on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.26'

      - name: Install envtest
        run: |
          go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
          setup-envtest use 1.28.x!

      - name: Run tests
        run: make test

      - name: Upload coverage
        uses: codecov/codecov-action@v3
        with:
          files: ./coverage.out
```

## Best Practices

### ✅ DO

- Use `testutil.EnvTestSuite` as base for all integration tests
- Use `Eventually()` for all async assertions
- Create fresh mocks in `SetupTest()`
- Verify mock expectations in `TearDownTest()`
- Use test constants from `testutil` package
- Test both happy and error paths
- Keep tests focused (one scenario per test)

### ❌ DON'T

- Use `time.Sleep()` - always use `Eventually()`
- Share mocks between tests - create fresh in `SetupTest()`
- Ignore mock expectations - always call `AssertExpectations()`
- Hard-code test data - use fixtures and constants
- Skip cleanup - remove test resources in `TearDownTest()`
- Test multiple scenarios in one test - split into separate tests

## Example Test

See `pkg/controller/gateway_controller_test.go` for a complete example following all best practices.

## References

- [Envtest Documentation](https://book.kubebuilder.io/reference/envtest.html)
- [Testify Documentation](https://github.com/stretchr/testify)
- [Controller-Runtime Testing](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest)
