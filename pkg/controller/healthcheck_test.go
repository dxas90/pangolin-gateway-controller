package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPangolinClientForHealth implements pangolin.ClientInterface with configurable Ping behavior.
type mockPangolinClientForHealth struct {
	pangolin.ClientInterface
	pingErr error
}

func (m *mockPangolinClientForHealth) Ping(ctx context.Context) error {
	return m.pingErr
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
	connErr := fmt.Errorf("ping: API unreachable: %w", fmt.Errorf("connection refused"))
	mock := &mockPangolinClientForHealth{pingErr: connErr}
	checker := NewPangolinReadyChecker(mock)
	require.NotNil(t, checker)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	err := checker(req)
	assert.Error(t, err)
}
