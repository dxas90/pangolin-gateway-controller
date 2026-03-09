package testutil

import (
	"context"

	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/stretchr/testify/mock"
)

// MockPangolinClient is a mock implementation of the Pangolin API client for testing.
type MockPangolinClient struct {
	mock.Mock
}

// Ensure MockPangolinClient implements the pangolin.ClientInterface
var _ pangolin.ClientInterface = (*MockPangolinClient)(nil)

// PickSiteDefaults mocks the PickSiteDefaults method.
func (m *MockPangolinClient) PickSiteDefaults(ctx context.Context) (*pangolin.SiteDefaults, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*pangolin.SiteDefaults), args.Error(1)
}

// CreateSite mocks the CreateSite method.
func (m *MockPangolinClient) CreateSite(ctx context.Context, site *pangolin.Site) (*pangolin.Site, error) {
	args := m.Called(ctx, site)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*pangolin.Site), args.Error(1)
}

// ListSites mocks the ListSites method.
func (m *MockPangolinClient) ListSites(ctx context.Context) ([]pangolin.Site, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]pangolin.Site), args.Error(1)
}

// GetSite mocks the GetSite method.
func (m *MockPangolinClient) GetSite(ctx context.Context, siteID string) (*pangolin.Site, error) {
	args := m.Called(ctx, siteID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*pangolin.Site), args.Error(1)
}

// DeleteSite mocks the DeleteSite method.
func (m *MockPangolinClient) DeleteSite(ctx context.Context, siteID int) error {
	args := m.Called(ctx, siteID)
	return args.Error(0)
}

// CreateResource mocks the CreateResource method.
func (m *MockPangolinClient) CreateResource(ctx context.Context, resourceData map[string]interface{}) (map[string]interface{}, error) {
	args := m.Called(ctx, resourceData)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[string]interface{}), args.Error(1)
}

// ListResources mocks the ListResources method.
func (m *MockPangolinClient) ListResources(ctx context.Context) ([]map[string]interface{}, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]map[string]interface{}), args.Error(1)
}

// DeleteResource mocks the DeleteResource method.
func (m *MockPangolinClient) DeleteResource(ctx context.Context, resourceID string) error {
	args := m.Called(ctx, resourceID)
	return args.Error(0)
}

// UpdateResource mocks the UpdateResource method.
func (m *MockPangolinClient) UpdateResource(ctx context.Context, resourceID string, data map[string]interface{}) error {
	args := m.Called(ctx, resourceID, data)
	return args.Error(0)
}

// DisableSSO mocks the DisableSSO method.
func (m *MockPangolinClient) DisableSSO(ctx context.Context, resourceID string) error {
	args := m.Called(ctx, resourceID)
	return args.Error(0)
}

// CreateTargetRaw mocks the CreateTargetRaw method.
func (m *MockPangolinClient) CreateTargetRaw(ctx context.Context, resourceID string, targetData map[string]interface{}) (map[string]interface{}, error) {
	args := m.Called(ctx, resourceID, targetData)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[string]interface{}), args.Error(1)
}

// ListTargetsRaw mocks the ListTargetsRaw method.
func (m *MockPangolinClient) ListTargetsRaw(ctx context.Context, resourceID string) ([]map[string]interface{}, error) {
	args := m.Called(ctx, resourceID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]map[string]interface{}), args.Error(1)
}

// DeleteTarget mocks the DeleteTarget method.
func (m *MockPangolinClient) DeleteTarget(ctx context.Context, targetID string) error {
	args := m.Called(ctx, targetID)
	return args.Error(0)
}

// ListDomains mocks the ListDomains method.
func (m *MockPangolinClient) ListDomains(ctx context.Context) ([]map[string]interface{}, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]map[string]interface{}), args.Error(1)
}

// GetServerVersion mocks the GetServerVersion method.
func (m *MockPangolinClient) GetServerVersion(ctx context.Context, newtEndpoint, newtID, newtSecret string) (string, error) {
	args := m.Called(ctx, newtEndpoint, newtID, newtSecret)
	return args.String(0), args.Error(1)
}

// NewMockPangolinClient creates a new mock Pangolin client.
func NewMockPangolinClient() *MockPangolinClient {
	return &MockPangolinClient{}
}
