package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPangolinClientForHealth implements pangolin.ClientInterface with configurable ListSites behavior.
type mockPangolinClientForHealth struct {
	pangolin.ClientInterface
	listSitesErr error
}

func (m *mockPangolinClientForHealth) ListSites(ctx context.Context) ([]pangolin.Site, error) {
	if m.listSitesErr != nil {
		return nil, m.listSitesErr
	}
	return []pangolin.Site{{Name: "test"}}, nil
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
		listSitesErr: pangolin.ErrCircuitOpen,
	}
	checker := NewPangolinReadyChecker(mock)
	require.NotNil(t, checker)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	err := checker(req)
	assert.Error(t, err)
	assert.ErrorIs(t, err, pangolin.ErrCircuitOpen)
}

func TestNewPangolinReadyChecker_APIError(t *testing.T) {
	mock := &mockPangolinClientForHealth{
		listSitesErr: &pangolin.PangolinAPIError{
			StatusCode: 500,
			Method:     "GET",
			Endpoint:   "/org/test/sites",
			Message:    "server error",
		},
	}
	checker := NewPangolinReadyChecker(mock)
	require.NotNil(t, checker)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	err := checker(req)
	assert.Error(t, err)
}
