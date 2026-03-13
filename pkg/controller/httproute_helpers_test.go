package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// internalMockPangolin is a mock for pangolin.ClientInterface used in internal package tests.
type internalMockPangolin struct {
	mock.Mock
}

func (m *internalMockPangolin) PickSiteDefaults(ctx context.Context) (*pangolin.SiteDefaults, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*pangolin.SiteDefaults), args.Error(1)
}
func (m *internalMockPangolin) CreateSite(ctx context.Context, site *pangolin.Site) (*pangolin.Site, error) {
	args := m.Called(ctx, site)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*pangolin.Site), args.Error(1)
}
func (m *internalMockPangolin) GetSite(ctx context.Context, siteID string) (*pangolin.Site, error) {
	args := m.Called(ctx, siteID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*pangolin.Site), args.Error(1)
}
func (m *internalMockPangolin) ListSites(ctx context.Context) ([]pangolin.Site, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]pangolin.Site), args.Error(1)
}
func (m *internalMockPangolin) DeleteSite(ctx context.Context, siteID int) error {
	return m.Called(ctx, siteID).Error(0)
}
func (m *internalMockPangolin) CreateResource(ctx context.Context, data map[string]interface{}) (map[string]interface{}, error) {
	args := m.Called(ctx, data)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[string]interface{}), args.Error(1)
}
func (m *internalMockPangolin) ListResources(ctx context.Context) ([]map[string]interface{}, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]map[string]interface{}), args.Error(1)
}
func (m *internalMockPangolin) UpdateResource(ctx context.Context, resourceID string, data map[string]interface{}) error {
	return m.Called(ctx, resourceID, data).Error(0)
}
func (m *internalMockPangolin) DeleteResource(ctx context.Context, resourceID string) error {
	return m.Called(ctx, resourceID).Error(0)
}
func (m *internalMockPangolin) DisableSSO(ctx context.Context, resourceID string) error {
	return m.Called(ctx, resourceID).Error(0)
}
func (m *internalMockPangolin) CreateTargetRaw(ctx context.Context, resourceID string, data map[string]interface{}) (map[string]interface{}, error) {
	args := m.Called(ctx, resourceID, data)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(map[string]interface{}), args.Error(1)
}
func (m *internalMockPangolin) ListTargetsRaw(ctx context.Context, resourceID string) ([]map[string]interface{}, error) {
	args := m.Called(ctx, resourceID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]map[string]interface{}), args.Error(1)
}
func (m *internalMockPangolin) DeleteTarget(ctx context.Context, targetID string) error {
	return m.Called(ctx, targetID).Error(0)
}
func (m *internalMockPangolin) ListDomains(ctx context.Context) ([]map[string]interface{}, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]map[string]interface{}), args.Error(1)
}
func (m *internalMockPangolin) GetServerVersion(ctx context.Context, newtEndpoint, newtID, newtSecret string) (string, error) {
	args := m.Called(ctx, newtEndpoint, newtID, newtSecret)
	return args.String(0), args.Error(1)
}

var _ pangolin.ClientInterface = (*internalMockPangolin)(nil)

// --- extractSubdomainFromDomains ---

func TestExtractSubdomainFromDomains_SuffixMatch(t *testing.T) {
	domains := []map[string]interface{}{
		{"baseDomain": "example.com", "domainId": "dom-1"},
	}

	subdomain, domainID, err := extractSubdomainFromDomains("app.example.com", domains)
	assert.NoError(t, err)
	assert.Equal(t, "app", subdomain)
	assert.Equal(t, "dom-1", domainID)
}

func TestExtractSubdomainFromDomains_ExactMatch(t *testing.T) {
	domains := []map[string]interface{}{
		{"baseDomain": "example.com", "domainId": "dom-1"},
	}

	subdomain, domainID, err := extractSubdomainFromDomains("example.com", domains)
	assert.NoError(t, err)
	assert.Equal(t, "example.com", subdomain) // exact match returns hostname as-is
	assert.Equal(t, "dom-1", domainID)
}

func TestExtractSubdomainFromDomains_NoMatch(t *testing.T) {
	domains := []map[string]interface{}{
		{"baseDomain": "example.com", "domainId": "dom-1"},
	}

	_, _, err := extractSubdomainFromDomains("app.other.com", domains)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no matching domain")
}

func TestExtractSubdomainFromDomains_EmptyDomains(t *testing.T) {
	_, _, err := extractSubdomainFromDomains("app.example.com", nil)
	assert.Error(t, err)
}

func TestExtractSubdomainFromDomains_MissingFields(t *testing.T) {
	tests := []struct {
		name    string
		domains []map[string]interface{}
	}{
		{
			name:    "missing baseDomain",
			domains: []map[string]interface{}{{"domainId": "dom-1"}},
		},
		{
			name:    "missing domainId",
			domains: []map[string]interface{}{{"baseDomain": "example.com"}},
		},
		{
			name:    "empty baseDomain",
			domains: []map[string]interface{}{{"baseDomain": "", "domainId": "dom-1"}},
		},
		{
			name:    "empty domainId",
			domains: []map[string]interface{}{{"baseDomain": "example.com", "domainId": ""}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := extractSubdomainFromDomains("app.example.com", tt.domains)
			assert.Error(t, err)
		})
	}
}

func TestExtractSubdomainFromDomains_MultipleDomains(t *testing.T) {
	domains := []map[string]interface{}{
		{"baseDomain": "other.com", "domainId": "dom-1"},
		{"baseDomain": "example.com", "domainId": "dom-2"},
	}

	subdomain, domainID, err := extractSubdomainFromDomains("api.example.com", domains)
	assert.NoError(t, err)
	assert.Equal(t, "api", subdomain)
	assert.Equal(t, "dom-2", domainID)
}

func TestExtractSubdomainFromDomains_DeepSubdomain(t *testing.T) {
	domains := []map[string]interface{}{
		{"baseDomain": "example.com", "domainId": "dom-1"},
	}

	subdomain, domainID, err := extractSubdomainFromDomains("a.b.c.example.com", domains)
	assert.NoError(t, err)
	assert.Equal(t, "a.b.c", subdomain)
	assert.Equal(t, "dom-1", domainID)
}

// --- numericToString ---

func TestNumericToString(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected string
	}{
		{"float64", float64(42), "42"},
		{"float64 with decimal", float64(3.14), "3"},
		{"int", int(100), "100"},
		{"int64", int64(9999), "9999"},
		{"string", "hello", "hello"},
		{"bool", true, "true"},
		{"nil", nil, "<nil>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, numericToString(tt.input))
		})
	}
}

// --- sanitizeEventMessage ---

func TestSanitizeEventMessage_Short(t *testing.T) {
	err := errors.New("something went wrong")
	result := sanitizeEventMessage(err)
	assert.Equal(t, "something went wrong", result)
}

func TestSanitizeEventMessage_ExactlyAtLimit(t *testing.T) {
	msg := strings.Repeat("a", 200)
	err := errors.New(msg)
	result := sanitizeEventMessage(err)
	assert.Equal(t, msg, result)
	assert.Len(t, result, 200)
}

func TestSanitizeEventMessage_TruncatesLong(t *testing.T) {
	msg := strings.Repeat("a", 300)
	err := errors.New(msg)
	result := sanitizeEventMessage(err)
	assert.Len(t, result, 203) // 200 + "..."
	assert.True(t, strings.HasSuffix(result, "..."))
}

// --- findExistingResourceBySubdomainFromMap ---

func TestFindExistingResourceBySubdomainFromMap_NilMap(t *testing.T) {
	r := &HTTPRouteReconciler{}
	result := r.findExistingResourceBySubdomainFromMap("test.example.com", nil)
	assert.Empty(t, result)
}

func TestFindExistingResourceBySubdomainFromMap_Found(t *testing.T) {
	r := &HTTPRouteReconciler{}
	m := map[string]string{
		"test.example.com":  "res-123",
		"other.example.com": "res-456",
	}
	result := r.findExistingResourceBySubdomainFromMap("test.example.com", m)
	assert.Equal(t, "res-123", result)
}

func TestFindExistingResourceBySubdomainFromMap_NotFound(t *testing.T) {
	r := &HTTPRouteReconciler{}
	m := map[string]string{
		"other.example.com": "res-456",
	}
	result := r.findExistingResourceBySubdomainFromMap("test.example.com", m)
	assert.Empty(t, result)
}

func TestFindExistingResourceBySubdomainFromMap_EmptyMap(t *testing.T) {
	r := &HTTPRouteReconciler{}
	result := r.findExistingResourceBySubdomainFromMap("test.example.com", map[string]string{})
	assert.Empty(t, result)
}

// --- extractHeadersFromRoute ---

func TestExtractHeadersFromRoute_NoRules(t *testing.T) {
	r := &HTTPRouteReconciler{}
	route := &gatewayv1.HTTPRoute{
		Spec: gatewayv1.HTTPRouteSpec{},
	}
	log := ctrl.Log.WithName("test")
	headers := r.extractHeadersFromRoute(route, log)
	assert.Empty(t, headers)
}

func TestExtractHeadersFromRoute_NoFilters(t *testing.T) {
	r := &HTTPRouteReconciler{}
	route := &gatewayv1.HTTPRoute{
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{
					Filters: []gatewayv1.HTTPRouteFilter{},
				},
			},
		},
	}
	log := ctrl.Log.WithName("test")
	headers := r.extractHeadersFromRoute(route, log)
	assert.Empty(t, headers)
}

func TestExtractHeadersFromRoute_NonHeaderFilter(t *testing.T) {
	r := &HTTPRouteReconciler{}
	route := &gatewayv1.HTTPRoute{
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{
					Filters: []gatewayv1.HTTPRouteFilter{
						{
							Type: gatewayv1.HTTPRouteFilterURLRewrite,
						},
					},
				},
			},
		},
	}
	log := ctrl.Log.WithName("test")
	headers := r.extractHeadersFromRoute(route, log)
	assert.Empty(t, headers)
}

func TestExtractHeadersFromRoute_WithHeaders(t *testing.T) {
	r := &HTTPRouteReconciler{}
	route := &gatewayv1.HTTPRoute{
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{
					Filters: []gatewayv1.HTTPRouteFilter{
						{
							Type: gatewayv1.HTTPRouteFilterRequestHeaderModifier,
							RequestHeaderModifier: &gatewayv1.HTTPHeaderFilter{
								Add: []gatewayv1.HTTPHeader{
									{Name: "X-Custom-Header", Value: "custom-value"},
									{Name: "X-Another", Value: "another-value"},
								},
							},
						},
					},
				},
			},
		},
	}
	log := ctrl.Log.WithName("test")
	headers := r.extractHeadersFromRoute(route, log)
	assert.Len(t, headers, 2)
	assert.Equal(t, "X-Custom-Header", headers[0]["name"])
	assert.Equal(t, "custom-value", headers[0]["value"])
	assert.Equal(t, "X-Another", headers[1]["name"])
	assert.Equal(t, "another-value", headers[1]["value"])
}

func TestExtractHeadersFromRoute_NilModifier(t *testing.T) {
	r := &HTTPRouteReconciler{}
	route := &gatewayv1.HTTPRoute{
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{
					Filters: []gatewayv1.HTTPRouteFilter{
						{
							Type:                  gatewayv1.HTTPRouteFilterRequestHeaderModifier,
							RequestHeaderModifier: nil,
						},
					},
				},
			},
		},
	}
	log := ctrl.Log.WithName("test")
	headers := r.extractHeadersFromRoute(route, log)
	assert.Empty(t, headers)
}

func TestExtractHeadersFromRoute_MultipleRules(t *testing.T) {
	r := &HTTPRouteReconciler{}
	route := &gatewayv1.HTTPRoute{
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{
					Filters: []gatewayv1.HTTPRouteFilter{
						{
							Type: gatewayv1.HTTPRouteFilterRequestHeaderModifier,
							RequestHeaderModifier: &gatewayv1.HTTPHeaderFilter{
								Add: []gatewayv1.HTTPHeader{
									{Name: "X-Rule1", Value: "val1"},
								},
							},
						},
					},
				},
				{
					Filters: []gatewayv1.HTTPRouteFilter{
						{
							Type: gatewayv1.HTTPRouteFilterRequestHeaderModifier,
							RequestHeaderModifier: &gatewayv1.HTTPHeaderFilter{
								Add: []gatewayv1.HTTPHeader{
									{Name: "X-Rule2", Value: "val2"},
								},
							},
						},
					},
				},
			},
		},
	}
	log := ctrl.Log.WithName("test")
	headers := r.extractHeadersFromRoute(route, log)
	assert.Len(t, headers, 2)
	assert.Equal(t, "X-Rule1", headers[0]["name"])
	assert.Equal(t, "X-Rule2", headers[1]["name"])
}

// --- findExistingResourceBySubdomainAPI ---

func TestFindExistingResourceBySubdomainAPI_Found(t *testing.T) {
	mockClient := new(internalMockPangolin)
	r := &HTTPRouteReconciler{PangolinClient: mockClient}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	mockClient.On("ListResources", ctx).Return([]map[string]interface{}{
		{"name": "other.example.com", "resourceId": "res-111"},
		{"name": "test.example.com", "resourceId": "res-222"},
	}, nil)

	id, err := r.findExistingResourceBySubdomainAPI(ctx, "test.example.com", log)
	assert.NoError(t, err)
	assert.Equal(t, "res-222", id)
}

func TestFindExistingResourceBySubdomainAPI_NotFound(t *testing.T) {
	mockClient := new(internalMockPangolin)
	r := &HTTPRouteReconciler{PangolinClient: mockClient}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	mockClient.On("ListResources", ctx).Return([]map[string]interface{}{
		{"name": "other.example.com", "resourceId": "res-111"},
	}, nil)

	id, err := r.findExistingResourceBySubdomainAPI(ctx, "test.example.com", log)
	assert.NoError(t, err)
	assert.Empty(t, id)
}

func TestFindExistingResourceBySubdomainAPI_ListError(t *testing.T) {
	mockClient := new(internalMockPangolin)
	r := &HTTPRouteReconciler{PangolinClient: mockClient}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	mockClient.On("ListResources", ctx).Return(nil, errors.New("api error"))

	id, err := r.findExistingResourceBySubdomainAPI(ctx, "test.example.com", log)
	assert.Error(t, err)
	assert.Empty(t, id)
}

func TestFindExistingResourceBySubdomainAPI_NilResourceID(t *testing.T) {
	mockClient := new(internalMockPangolin)
	r := &HTTPRouteReconciler{PangolinClient: mockClient}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	mockClient.On("ListResources", ctx).Return([]map[string]interface{}{
		{"name": "test.example.com", "resourceId": nil},
	}, nil)

	id, err := r.findExistingResourceBySubdomainAPI(ctx, "test.example.com", log)
	assert.NoError(t, err)
	assert.Empty(t, id)
}

func TestFindExistingResourceBySubdomainAPI_NumericResourceID(t *testing.T) {
	mockClient := new(internalMockPangolin)
	r := &HTTPRouteReconciler{PangolinClient: mockClient}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	mockClient.On("ListResources", ctx).Return([]map[string]interface{}{
		{"name": "test.example.com", "resourceId": float64(42)},
	}, nil)

	id, err := r.findExistingResourceBySubdomainAPI(ctx, "test.example.com", log)
	assert.NoError(t, err)
	assert.Equal(t, "42", id)
}

// --- createPangolinResourceForHostname ---

func TestCreatePangolinResourceForHostname_Success(t *testing.T) {
	mockClient := new(internalMockPangolin)
	r := &HTTPRouteReconciler{PangolinClient: mockClient}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	domains := []map[string]interface{}{
		{"baseDomain": "example.com", "domainId": "dom-1"},
	}

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec:       gatewayv1.HTTPRouteSpec{},
	}

	mockClient.On("CreateResource", ctx, mock.AnythingOfType("map[string]interface {}")).Return(map[string]interface{}{
		"resourceId": "res-new-123",
	}, nil)

	id, err := r.createPangolinResourceForHostname(ctx, route, "app.example.com", "app.example.com", domains, log)
	assert.NoError(t, err)
	assert.Equal(t, "res-new-123", id)
}

func TestCreatePangolinResourceForHostname_CreateError(t *testing.T) {
	mockClient := new(internalMockPangolin)
	r := &HTTPRouteReconciler{PangolinClient: mockClient}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	domains := []map[string]interface{}{
		{"baseDomain": "example.com", "domainId": "dom-1"},
	}

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec:       gatewayv1.HTTPRouteSpec{},
	}

	mockClient.On("CreateResource", ctx, mock.AnythingOfType("map[string]interface {}")).Return(nil, errors.New("create failed"))

	id, err := r.createPangolinResourceForHostname(ctx, route, "app.example.com", "app.example.com", domains, log)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create resource")
	assert.Empty(t, id)
}

func TestCreatePangolinResourceForHostname_NoResourceIDInResponse(t *testing.T) {
	mockClient := new(internalMockPangolin)
	r := &HTTPRouteReconciler{PangolinClient: mockClient}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	domains := []map[string]interface{}{
		{"baseDomain": "example.com", "domainId": "dom-1"},
	}

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec:       gatewayv1.HTTPRouteSpec{},
	}

	mockClient.On("CreateResource", ctx, mock.AnythingOfType("map[string]interface {}")).Return(map[string]interface{}{}, nil)

	id, err := r.createPangolinResourceForHostname(ctx, route, "app.example.com", "app.example.com", domains, log)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no resourceId")
	assert.Empty(t, id)
}

func TestCreatePangolinResourceForHostname_NoDomainMatch(t *testing.T) {
	mockClient := new(internalMockPangolin)
	r := &HTTPRouteReconciler{PangolinClient: mockClient}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	// When domains are non-empty but no domain matches, return an error (don't silently fall back).
	// This is the correct behavior: if the caller provided real domain data, a non-matching
	// hostname is likely misconfigured and should not be silently routed to a placeholder domain.
	domains := []map[string]interface{}{
		{"baseDomain": "other.com", "domainId": "dom-1"},
	}

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec:       gatewayv1.HTTPRouteSpec{},
	}

	id, err := r.createPangolinResourceForHostname(ctx, route, "app.example.com", "app.example.com", domains, log)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "does not match any configured Pangolin domain")
	assert.Empty(t, id)
	// No CreateResource call should have been made
	mockClient.AssertNotCalled(t, "CreateResource")
}

func TestCreatePangolinResourceForHostname_WithHeaders(t *testing.T) {
	mockClient := new(internalMockPangolin)
	r := &HTTPRouteReconciler{PangolinClient: mockClient}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	domains := []map[string]interface{}{
		{"baseDomain": "example.com", "domainId": "dom-1"},
	}

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{
					Filters: []gatewayv1.HTTPRouteFilter{
						{
							Type: gatewayv1.HTTPRouteFilterRequestHeaderModifier,
							RequestHeaderModifier: &gatewayv1.HTTPHeaderFilter{
								Add: []gatewayv1.HTTPHeader{
									{Name: "X-Custom", Value: "val"},
								},
							},
						},
					},
				},
			},
		},
	}

	mockClient.On("CreateResource", ctx, mock.AnythingOfType("map[string]interface {}")).Return(map[string]interface{}{
		"resourceId": "res-hdr",
	}, nil)
	mockClient.On("UpdateResource", ctx, "res-hdr", mock.AnythingOfType("map[string]interface {}")).Return(nil)

	id, err := r.createPangolinResourceForHostname(ctx, route, "app.example.com", "app.example.com", domains, log)
	assert.NoError(t, err)
	assert.Equal(t, "res-hdr", id)
	mockClient.AssertCalled(t, "UpdateResource", ctx, "res-hdr", mock.AnythingOfType("map[string]interface {}"))
}

func TestCreatePangolinResourceForHostname_WithDisableSSO(t *testing.T) {
	mockClient := new(internalMockPangolin)
	r := &HTTPRouteReconciler{PangolinClient: mockClient}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	domains := []map[string]interface{}{
		{"baseDomain": "example.com", "domainId": "dom-1"},
	}

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-route",
			Namespace:   "default",
			Annotations: map[string]string{"gateway.pangolin.net/disable-sso": "true"},
		},
		Spec: gatewayv1.HTTPRouteSpec{},
	}

	mockClient.On("CreateResource", ctx, mock.AnythingOfType("map[string]interface {}")).Return(map[string]interface{}{
		"resourceId": "res-sso",
	}, nil)
	mockClient.On("DisableSSO", ctx, "res-sso").Return(nil)

	id, err := r.createPangolinResourceForHostname(ctx, route, "app.example.com", "app.example.com", domains, log)
	assert.NoError(t, err)
	assert.Equal(t, "res-sso", id)
	mockClient.AssertCalled(t, "DisableSSO", ctx, "res-sso")
}

func TestCreatePangolinResourceForHostname_DisableSSOError(t *testing.T) {
	mockClient := new(internalMockPangolin)
	r := &HTTPRouteReconciler{PangolinClient: mockClient}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	domains := []map[string]interface{}{
		{"baseDomain": "example.com", "domainId": "dom-1"},
	}

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-route",
			Namespace:   "default",
			Annotations: map[string]string{"gateway.pangolin.net/disable-sso": "true"},
		},
		Spec: gatewayv1.HTTPRouteSpec{},
	}

	mockClient.On("CreateResource", ctx, mock.AnythingOfType("map[string]interface {}")).Return(map[string]interface{}{
		"resourceId": "res-sso-err",
	}, nil)
	mockClient.On("DisableSSO", ctx, "res-sso-err").Return(errors.New("sso error"))

	// Should still succeed even if DisableSSO fails (error is logged, not returned)
	id, err := r.createPangolinResourceForHostname(ctx, route, "app.example.com", "app.example.com", domains, log)
	assert.NoError(t, err)
	assert.Equal(t, "res-sso-err", id)
}

func TestCreatePangolinResourceForHostname_UpdateHeadersError(t *testing.T) {
	mockClient := new(internalMockPangolin)
	r := &HTTPRouteReconciler{PangolinClient: mockClient}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	domains := []map[string]interface{}{
		{"baseDomain": "example.com", "domainId": "dom-1"},
	}

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{
					Filters: []gatewayv1.HTTPRouteFilter{
						{
							Type: gatewayv1.HTTPRouteFilterRequestHeaderModifier,
							RequestHeaderModifier: &gatewayv1.HTTPHeaderFilter{
								Add: []gatewayv1.HTTPHeader{
									{Name: "X-Fail", Value: "val"},
								},
							},
						},
					},
				},
			},
		},
	}

	mockClient.On("CreateResource", ctx, mock.AnythingOfType("map[string]interface {}")).Return(map[string]interface{}{
		"resourceId": "res-upd-err",
	}, nil)
	mockClient.On("UpdateResource", ctx, "res-upd-err", mock.AnythingOfType("map[string]interface {}")).Return(errors.New("update failed"))

	// Should still succeed even if UpdateResource fails (error is logged, not returned)
	id, err := r.createPangolinResourceForHostname(ctx, route, "app.example.com", "app.example.com", domains, log)
	assert.NoError(t, err)
	assert.Equal(t, "res-upd-err", id)
}
