package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// ---------------------------------------------------------------------------
// isSiteGoneError
// ---------------------------------------------------------------------------

func TestIsSiteGoneError_NilError(t *testing.T) {
	assert.False(t, isSiteGoneError(nil))
}

func TestIsSiteGoneError_GenericError(t *testing.T) {
	assert.False(t, isSiteGoneError(errors.New("some error")))
}

func TestIsSiteGoneError_404WithSiteMessage(t *testing.T) {
	err := &pangolin.PangolinAPIError{
		StatusCode: 404,
		Method:     "GET",
		Endpoint:   "/site/42",
		Message:    "Site with ID 42 not found",
	}
	assert.True(t, isSiteGoneError(err))
}

func TestIsSiteGoneError_404WithoutSiteMessage(t *testing.T) {
	err := &pangolin.PangolinAPIError{
		StatusCode: 404,
		Method:     "DELETE",
		Endpoint:   "/public-resource/res-1",
		Message:    "Resource not found",
	}
	assert.False(t, isSiteGoneError(err), "404 without 'Site' in message must not be treated as site-gone")
}

func TestIsSiteGoneError_500WithSiteMessage(t *testing.T) {
	err := &pangolin.PangolinAPIError{
		StatusCode: 500,
		Method:     "GET",
		Endpoint:   "/site/42",
		Message:    "Site with ID 42 not found",
	}
	assert.False(t, isSiteGoneError(err), "non-404 status must not be treated as site-gone")
}

func TestIsSiteGoneError_WrappedPangolinError(t *testing.T) {
	inner := &pangolin.PangolinAPIError{
		StatusCode: 404,
		Method:     "GET",
		Endpoint:   "/site/99",
		Message:    "Site with ID 99 not found",
	}
	wrapped := errors.New("reconcile failed: " + inner.Error())
	// wrapped with fmt.Errorf %w would unwrap — plain errors.New does not
	assert.False(t, isSiteGoneError(wrapped))
}

// ---------------------------------------------------------------------------
// createPangolinResourceForHostname — conflict adoption path
// ---------------------------------------------------------------------------

func TestCreatePangolinResourceForHostname_ConflictAdoptsExisting(t *testing.T) {
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

	// CreateResource returns 409 conflict
	conflictErr := &pangolin.PangolinAPIError{StatusCode: 409, Method: "PUT", Endpoint: "/org/test-org/public-resource", Message: "conflict"}
	mockClient.On("CreateResource", ctx, mock.AnythingOfType("map[string]interface {}")).Return(nil, conflictErr)

	// ListResources returns the already-existing resource
	mockClient.On("ListResources", ctx).Return([]map[string]interface{}{
		{"resourceId": "adopted-res-1", "name": "app.example.com"},
	}, nil)

	id, err := r.createPangolinResourceForHostname(ctx, route, "app.example.com", "app.example.com", domains, log)
	require.NoError(t, err)
	assert.Equal(t, "adopted-res-1", id)
}

func TestCreatePangolinResourceForHostname_ConflictListFails_ReturnsError(t *testing.T) {
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

	conflictErr := &pangolin.PangolinAPIError{StatusCode: 409, Method: "PUT", Endpoint: "/org/x/public-resource", Message: "conflict"}
	mockClient.On("CreateResource", ctx, mock.AnythingOfType("map[string]interface {}")).Return(nil, conflictErr)
	// List returns error — can't adopt
	mockClient.On("ListResources", ctx).Return(nil, errors.New("list unavailable"))

	_, err := r.createPangolinResourceForHostname(ctx, route, "app.example.com", "app.example.com", domains, log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create resource")
}

func TestCreatePangolinResourceForHostname_EmptyDomains_UsesHostnameAsFallback(t *testing.T) {
	mockClient := new(internalMockPangolin)
	r := &HTTPRouteReconciler{PangolinClient: mockClient}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec:       gatewayv1.HTTPRouteSpec{},
	}

	mockClient.On("CreateResource", ctx, mock.AnythingOfType("map[string]interface {}")).Return(map[string]interface{}{
		"resourceId": "fallback-res",
	}, nil)

	id, err := r.createPangolinResourceForHostname(ctx, route, "app.example.com", "app.example.com", nil /* empty domains */, log)
	require.NoError(t, err)
	assert.Equal(t, "fallback-res", id)

	// Verify the resourceData sent uses the hostname as subdomain
	call := mockClient.Calls[0]
	data, ok := call.Arguments[1].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "app.example.com", data["subdomain"])
}

// ---------------------------------------------------------------------------
// reconcileTargets (HTTPRouteReconciler) — unit tests with fake k8s client
// ---------------------------------------------------------------------------

// buildScheme returns a runtime.Scheme with corev1 and gatewayv1 registered.
func buildScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, gatewayv1.Install(s))
	return s
}

func TestHTTPRoute_ReconcileTargets_InvalidSiteID_ReturnsError(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := buildScheme(t)
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{BackendRefs: []gatewayv1.HTTPBackendRef{{BackendRef: gatewayv1.BackendRef{BackendObjectReference: gatewayv1.BackendObjectReference{Name: "svc"}}}}},
			},
		},
	}

	err := r.reconcileTargets(ctx, route, "res-1", "not-a-number", log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid site ID")
}

func TestHTTPRoute_ReconcileTargets_ListTargetsError_ReturnsError(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := buildScheme(t)
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{{BackendRefs: []gatewayv1.HTTPBackendRef{}}},
		},
	}

	mockClient.On("ListTargetsRaw", ctx, "res-1").Return(nil, errors.New("api down"))

	err := r.reconcileTargets(ctx, route, "res-1", "42", log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list existing targets")
}

func TestHTTPRoute_ReconcileTargets_NoRules_NoTargets_Succeeds(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := buildScheme(t)
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec:       gatewayv1.HTTPRouteSpec{Rules: []gatewayv1.HTTPRouteRule{}},
	}

	// No existing targets
	mockClient.On("ListTargetsRaw", ctx, "res-1").Return([]map[string]interface{}{}, nil)

	err := r.reconcileTargets(ctx, route, "res-1", "42", log)
	require.NoError(t, err)
	// No CreateTargetRaw or DeleteTarget calls expected
	mockClient.AssertNotCalled(t, "CreateTargetRaw")
	mockClient.AssertNotCalled(t, "DeleteTarget")
}

func TestHTTPRoute_ReconcileTargets_CreatesTarget(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(20)
	scheme := buildScheme(t)

	// Create a Service for the backend
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-svc", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.96.1.100",
		},
	}
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{
					BackendRefs: []gatewayv1.HTTPBackendRef{
						{BackendRef: gatewayv1.BackendRef{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Name: "backend-svc",
								Port: &port,
							},
						}},
					},
				},
			},
		},
	}

	// No existing targets
	mockClient.On("ListTargetsRaw", ctx, "res-1").Return([]map[string]interface{}{}, nil)
	mockClient.On("CreateTargetRaw", ctx, "res-1", mock.AnythingOfType("map[string]interface {}")).Return(
		map[string]interface{}{"targetId": "new-tgt-1"}, nil,
	)

	err := r.reconcileTargets(ctx, route, "res-1", "42", log)
	require.NoError(t, err)
	mockClient.AssertCalled(t, "CreateTargetRaw", ctx, "res-1", mock.AnythingOfType("map[string]interface {}"))

	// Inspect created target data
	call := mockClient.Calls[1]
	data, ok := call.Arguments[2].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "10.96.1.100", data["ip"])
	assert.Equal(t, 8080, data["port"])
	assert.Equal(t, 42, data["siteId"])
	assert.Equal(t, "/", data["path"])
}

func TestHTTPRoute_ReconcileTargets_ExistingTargetNoDrift_NotRecreated(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(20)
	scheme := buildScheme(t)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.1.100"},
	}
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{BackendRefs: []gatewayv1.HTTPBackendRef{
					{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "backend-svc",
							Port: &port,
						},
					}},
				}},
			},
		},
	}

	// Existing target matches exactly — no update needed
	existingTargets := []map[string]interface{}{
		{
			"targetId":      "existing-tgt",
			"ip":            "10.96.1.100",
			"port":          float64(8080),
			"siteId":        float64(42),
			"path":          "/",
			"pathMatchType": "prefix",
			"priority":      float64(110),
			"method":        "http",
		},
	}
	mockClient.On("ListTargetsRaw", ctx, "res-1").Return(existingTargets, nil)

	err := r.reconcileTargets(ctx, route, "res-1", "42", log)
	require.NoError(t, err)
	mockClient.AssertNotCalled(t, "CreateTargetRaw")
	mockClient.AssertNotCalled(t, "DeleteTarget")
}

func TestHTTPRoute_ReconcileTargets_PathMatchTypeDrift_ReplacesTarget(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(20)
	scheme := buildScheme(t)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.1.100"},
	}
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	pathType := gatewayv1.PathMatchExact
	pathVal := "/"
	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{
					Matches: []gatewayv1.HTTPRouteMatch{{
						Path: &gatewayv1.HTTPPathMatch{Type: &pathType, Value: &pathVal},
					}},
					BackendRefs: []gatewayv1.HTTPBackendRef{
						{BackendRef: gatewayv1.BackendRef{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Name: "backend-svc",
								Port: &port,
							},
						}},
					},
				},
			},
		},
	}

	// Existing target has wrong pathMatchType ("prefix" vs desired "exact")
	existingTargets := []map[string]interface{}{
		{
			"targetId":      "stale-tgt",
			"ip":            "10.96.1.100",
			"port":          float64(8080),
			"siteId":        float64(42),
			"path":          "/",
			"pathMatchType": "prefix", // drift!
			"priority":      float64(110),
			"method":        "http",
		},
	}
	mockClient.On("ListTargetsRaw", ctx, "res-1").Return(existingTargets, nil)
	mockClient.On("DeleteTarget", ctx, "stale-tgt").Return(nil)
	mockClient.On("CreateTargetRaw", ctx, "res-1", mock.AnythingOfType("map[string]interface {}")).Return(
		map[string]interface{}{"targetId": "new-tgt"}, nil,
	)

	err := r.reconcileTargets(ctx, route, "res-1", "42", log)
	require.NoError(t, err)
	mockClient.AssertCalled(t, "DeleteTarget", ctx, "stale-tgt")
	mockClient.AssertCalled(t, "CreateTargetRaw", ctx, "res-1", mock.AnythingOfType("map[string]interface {}"))
}

func TestHTTPRoute_ReconcileTargets_OrphanedTarget_Deleted(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(20)
	scheme := buildScheme(t)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.1.100"},
	}
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{BackendRefs: []gatewayv1.HTTPBackendRef{
					{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "backend-svc",
							Port: &port,
						},
					}},
				}},
			},
		},
	}

	// Orphaned target: same path "/" but different IP — not matched, should be deleted
	orphanTarget := map[string]interface{}{
		"targetId":      "orphan-tgt",
		"ip":            "10.96.9.99", // old pod IP
		"port":          float64(8080),
		"siteId":        float64(42),
		"path":          "/",
		"pathMatchType": "prefix",
		"priority":      float64(110),
		"method":        "http",
	}
	mockClient.On("ListTargetsRaw", ctx, "res-1").Return([]map[string]interface{}{orphanTarget}, nil)
	mockClient.On("CreateTargetRaw", ctx, "res-1", mock.AnythingOfType("map[string]interface {}")).Return(
		map[string]interface{}{"targetId": "new-tgt"}, nil,
	)
	mockClient.On("DeleteTarget", ctx, "orphan-tgt").Return(nil)

	err := r.reconcileTargets(ctx, route, "res-1", "42", log)
	require.NoError(t, err)
	mockClient.AssertCalled(t, "DeleteTarget", ctx, "orphan-tgt")
}

func TestHTTPRoute_ReconcileTargets_ServiceNotFound_ReturnsError(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := buildScheme(t)
	// No Service objects — lookup will fail
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{BackendRefs: []gatewayv1.HTTPBackendRef{
					{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "missing-svc",
							Port: &port,
						},
					}},
				}},
			},
		},
	}

	mockClient.On("ListTargetsRaw", ctx, "res-1").Return([]map[string]interface{}{}, nil)

	err := r.reconcileTargets(ctx, route, "res-1", "42", log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get service")
}

func TestHTTPRoute_ReconcileTargets_ServiceHeadlessNoClusterIP_ReturnsError(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := buildScheme(t)

	// Headless service — ClusterIP is "None"
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "headless-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "None"},
	}
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{BackendRefs: []gatewayv1.HTTPBackendRef{
					{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "headless-svc",
							Port: &port,
						},
					}},
				}},
			},
		},
	}

	mockClient.On("ListTargetsRaw", ctx, "res-1").Return([]map[string]interface{}{}, nil)

	err := r.reconcileTargets(ctx, route, "res-1", "42", log)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no ClusterIP")
}

func TestHTTPRoute_ReconcileTargets_WeightOverridesPriority(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(20)
	scheme := buildScheme(t)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.1.1"},
	}
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	port := gatewayv1.PortNumber(8080)
	weight := int32(75)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{BackendRefs: []gatewayv1.HTTPBackendRef{
					{
						BackendRef: gatewayv1.BackendRef{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Name: "backend-svc",
								Port: &port,
							},
							Weight: &weight,
						},
					},
				}},
			},
		},
	}

	mockClient.On("ListTargetsRaw", ctx, "res-1").Return([]map[string]interface{}{}, nil)
	mockClient.On("CreateTargetRaw", ctx, "res-1", mock.AnythingOfType("map[string]interface {}")).Return(
		map[string]interface{}{"targetId": "tgt-w"}, nil,
	)

	err := r.reconcileTargets(ctx, route, "res-1", "42", log)
	require.NoError(t, err)

	call := mockClient.Calls[1]
	data, ok := call.Arguments[2].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, 75, data["priority"], "weight must override computed priority")
}

func TestHTTPRoute_ReconcileTargets_PathMatchRegex_SetOnTarget(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(20)
	scheme := buildScheme(t)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "regex-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.2.2"},
	}
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	pathType := gatewayv1.PathMatchRegularExpression
	pathVal := "^/api/v[0-9]+"
	port := gatewayv1.PortNumber(9090)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{
					Matches: []gatewayv1.HTTPRouteMatch{{
						Path: &gatewayv1.HTTPPathMatch{Type: &pathType, Value: &pathVal},
					}},
					BackendRefs: []gatewayv1.HTTPBackendRef{
						{BackendRef: gatewayv1.BackendRef{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Name: "regex-svc",
								Port: &port,
							},
						}},
					},
				},
			},
		},
	}

	mockClient.On("ListTargetsRaw", ctx, "res-1").Return([]map[string]interface{}{}, nil)
	mockClient.On("CreateTargetRaw", ctx, "res-1", mock.AnythingOfType("map[string]interface {}")).Return(
		map[string]interface{}{"targetId": "regex-tgt"}, nil,
	)

	err := r.reconcileTargets(ctx, route, "res-1", "42", log)
	require.NoError(t, err)

	call := mockClient.Calls[1]
	data, ok := call.Arguments[2].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "regex", data["pathMatchType"])
	assert.Equal(t, "^/api/v[0-9]+", data["path"])
}

func TestHTTPRoute_ReconcileTargets_ContextCancelled_BeforeListTargets(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := buildScheme(t)
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
	}
	log := ctrl.Log.WithName("test")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{BackendRefs: []gatewayv1.HTTPBackendRef{
					{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "svc",
							Port: &port,
						},
					}},
				}},
			},
		},
	}

	// ListTargetsRaw will be called with the cancelled context; simulate context error
	mockClient.On("ListTargetsRaw", mock.Anything, "res-1").Return(nil, context.Canceled)

	err := r.reconcileTargets(ctx, route, "res-1", "42", log)
	require.Error(t, err)
}
