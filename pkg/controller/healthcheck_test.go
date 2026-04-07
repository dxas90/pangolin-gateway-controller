package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPangolinClientForHealth implements pangolin.ClientInterface with configurable Ping behavior.
type mockPangolinClientForHealth struct {
	pangolin.ClientInterface
	pingErr   error
	mu        sync.Mutex
	pingCalls int
}

func (m *mockPangolinClientForHealth) Ping(_ context.Context) error {
	m.mu.Lock()
	m.pingCalls++
	m.mu.Unlock()
	return m.pingErr
}

func (m *mockPangolinClientForHealth) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pingCalls
}

func TestNewPangolinReadyChecker_Healthy(t *testing.T) {
	mock := &mockPangolinClientForHealth{}
	checker := NewPangolinReadyChecker(mock)
	require.NotNil(t, checker)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	err := checker(req)
	assert.NoError(t, err)
}

func TestNewPangolinReadyChecker_Unhealthy(t *testing.T) {
	mock := &mockPangolinClientForHealth{
		pingErr: pangolin.ErrCircuitOpen,
	}
	checker := NewPangolinReadyChecker(mock)
	require.NotNil(t, checker)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	err := checker(req)
	assert.Error(t, err)
	assert.ErrorIs(t, err, pangolin.ErrCircuitOpen)
}

func TestNewPangolinReadyChecker_ConnError(t *testing.T) {
	connErr := fmt.Errorf("ping: API unreachable: %w", errors.New("connection refused"))
	mock := &mockPangolinClientForHealth{pingErr: connErr}
	checker := NewPangolinReadyChecker(mock)
	require.NotNil(t, checker)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	err := checker(req)
	assert.Error(t, err)
}

// TestNewPangolinReadyChecker_CachesSuccessResult verifies that a successful Ping result
// is reused within the success cache TTL without calling Ping again.
func TestNewPangolinReadyChecker_CachesSuccessResult(t *testing.T) {
	mock := &mockPangolinClientForHealth{}
	checker := NewPangolinReadyChecker(mock)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	_ = checker(req)
	_ = checker(req) // second call within TTL
	_ = checker(req) // third call within TTL

	assert.Equal(t, 1, mock.calls(), "Ping should be called only once within the success cache TTL")
}

// TestNewPangolinReadyChecker_ErrorCacheShorterTTL verifies that Ping is retried after
// the error-cache TTL expires, which is shorter than the success TTL.
func TestNewPangolinReadyChecker_ErrorCacheShorterTTL(t *testing.T) {
	mock := &mockPangolinClientForHealth{pingErr: errors.New("server error 503")}
	p := &pangolinReadyChecker{
		client:        mock,
		cacheTTL:      30 * time.Second,
		errorCacheTTL: 5 * time.Millisecond, // very short so we can expire it in the test
	}

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	err := p.check(req)
	assert.Error(t, err)
	assert.Equal(t, 1, mock.calls())

	// Wait for the error TTL to expire.
	time.Sleep(10 * time.Millisecond)

	err = p.check(req)
	assert.Error(t, err)
	assert.Equal(t, 2, mock.calls(), "Ping should be retried after the error cache TTL")
}

// TestNewPangolinReadyChecker_ConcurrentNoDuplicate verifies (under -race) that
// concurrent probes do not trigger duplicate Ping calls while one is in-flight.
func TestNewPangolinReadyChecker_ConcurrentNoDuplicate(t *testing.T) {
	// Use a slow Ping to maximise the chance that concurrent goroutines arrive
	// while the first probe is still in-flight.
	slow := make(chan struct{})
	mock := &mockPangolinClientForHealth{}
	origPing := mock.pingCalls // 0

	p := &pangolinReadyChecker{
		client:        mock,
		cacheTTL:      30 * time.Second,
		errorCacheTTL: 5 * time.Second,
	}

	// Intercept Ping via a wrapper that blocks until slow is closed.
	slowMock := &slowMockPangolinClient{inner: mock, gate: slow}
	p.client = slowMock

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_ = p.check(req)
		}()
	}

	// Give goroutines time to pile up, then unblock Ping.
	time.Sleep(10 * time.Millisecond)
	close(slow)
	wg.Wait()

	_ = origPing
	assert.Equal(t, 1, slowMock.calls(), "only one Ping should fire despite concurrent probes")
}

// slowMockPangolinClient wraps mockPangolinClientForHealth with a gate channel
// so tests can control when Ping returns.
type slowMockPangolinClient struct {
	pangolin.ClientInterface
	inner *mockPangolinClientForHealth
	gate  chan struct{}
	mu    sync.Mutex
	n     int
}

func (s *slowMockPangolinClient) Ping(ctx context.Context) error {
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
	select {
	case <-s.gate:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.inner.pingErr
}

func (s *slowMockPangolinClient) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}
