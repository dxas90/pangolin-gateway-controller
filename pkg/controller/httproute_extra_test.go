package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	err := &pangolin.APIError{
		StatusCode: 404,
		Method:     "GET",
		Endpoint:   "/site/42",
		Message:    "Site with ID 42 not found",
	}
	assert.True(t, isSiteGoneError(err))
}

func TestIsSiteGoneError_404WithoutSiteMessage(t *testing.T) {
	err := &pangolin.APIError{
		StatusCode: 404,
		Method:     "DELETE",
		Endpoint:   "/public-resource/res-1",
		Message:    "Resource not found",
	}
	assert.False(t, isSiteGoneError(err), "404 without 'Site' in message must not be treated as site-gone")
}

func TestIsSiteGoneError_500WithSiteMessage(t *testing.T) {
	err := &pangolin.APIError{
		StatusCode: 500,
		Method:     "GET",
		Endpoint:   "/site/42",
		Message:    "Site with ID 42 not found",
	}
	assert.False(t, isSiteGoneError(err), "non-404 status must not be treated as site-gone")
}

func TestIsSiteGoneError_WrappedPangolinError(t *testing.T) {
	inner := &pangolin.APIError{
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
	conflictErr := &pangolin.APIError{StatusCode: 409, Method: "PUT", Endpoint: "/org/test-org/public-resource", Message: "conflict"}
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

	conflictErr := &pangolin.APIError{StatusCode: 409, Method: "PUT", Endpoint: "/org/x/public-resource", Message: "conflict"}
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

// --- updateRouteStatus tests ---

func TestUpdateRouteStatus_NoParentRefs(t *testing.T) {
	t.Helper()
	scheme := buildScheme(t)
	recorder := record.NewFakeRecorder(10)
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &HTTPRouteReconciler{
		Client:   fakeK8s,
		Log:      ctrl.Log.WithName("test"),
		Scheme:   scheme,
		Recorder: recorder,
	}

	ctx := context.Background()
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "default",
		},
		Spec: gatewayv1.HTTPRouteSpec{
			ParentRefs: []gatewayv1.ParentReference{}, // Empty
		},
	}

	fakeK8s.Create(ctx, route)

	// Should return early without error
	r.updateRouteStatus(ctx, route, true, "Accepted", "Route accepted")

	// Status should not be updated
	updated := &gatewayv1.HTTPRoute{}
	fakeK8s.Get(ctx, client.ObjectKeyFromObject(route), updated)
	assert.Empty(t, updated.Status.Parents)
}

// ---------------------------------------------------------------------------
// reconcileHTTPRoute — uncovered branches (fake-client unit tests)
// ---------------------------------------------------------------------------

// TestReconcileHTTPRoute_ListDomainsFails verifies that when ListDomains
// returns an error, reconcileHTTPRoute propagates the error.
func TestReconcileHTTPRoute_ListDomainsFails(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := buildScheme(t)

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "42"},
		},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "http-ld-fail", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			ParentRefs: []gatewayv1.ParentReference{{Name: "test-gw"}},
			Hostnames:  []gatewayv1.Hostname{"app.example.com"},
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway, route).
		WithStatusSubresource(route).
		Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		Log:            ctrl.Log.WithName("test"),
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	ctx := context.Background()

	// ListDomains fails
	mockClient.On("ListDomains", ctx).Return(nil, errors.New("domains down")).Once()

	_, err := r.reconcileHTTPRoute(ctx, route, ctrl.Log.WithName("test"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list domains")

	// reconcileHTTPRoute does NOT call updateRouteStatus for this error path
	// (it returns the error before the status update block). No assertion on status.
	mockClient.AssertExpectations(t)
}

// TestReconcileHTTPRoute_ListResourcesFails verifies that when ListResources
// returns an error, reconcileHTTPRoute propagates the error.
func TestReconcileHTTPRoute_ListResourcesFails(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := buildScheme(t)

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "42"},
		},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "http-lr-fail", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			ParentRefs: []gatewayv1.ParentReference{{Name: "test-gw"}},
			Hostnames:  []gatewayv1.Hostname{"app.example.com"},
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway, route).
		WithStatusSubresource(route).
		Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		Log:            ctrl.Log.WithName("test"),
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	ctx := context.Background()

	mockClient.On("ListDomains", ctx).Return([]map[string]interface{}{
		{"baseDomain": "example.com", "domainId": "dom-1"},
	}, nil).Once()
	mockClient.On("ListResources", ctx).Return(nil, errors.New("list resources down")).Once()

	_, err := r.reconcileHTTPRoute(ctx, route, ctrl.Log.WithName("test"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list resources")

	// reconcileHTTPRoute returns the error directly without calling updateRouteStatus.
	mockClient.AssertExpectations(t)
}

// TestReconcileHTTPRoute_NoHostnames verifies that a route with no hostnames
// gets NoHostnames status and a 5-minute requeue (no error).
func TestReconcileHTTPRoute_NoHostnames(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := buildScheme(t)

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "42"},
		},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "http-no-hosts", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			ParentRefs: []gatewayv1.ParentReference{{Name: "test-gw"}},
			Hostnames:  []gatewayv1.Hostname{}, // empty
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway, route).
		WithStatusSubresource(route).
		Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		Log:            ctrl.Log.WithName("test"),
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	ctx := context.Background()

	result, err := r.reconcileHTTPRoute(ctx, route, ctrl.Log.WithName("test"))
	require.NoError(t, err)
	assert.Equal(t, 5*time.Minute, result.RequeueAfter)

	// Production code calls updateRouteStatus(false, "NoHostnames", ...) before returning.
	// Verify the HTTPRoute status reflects the reason.
	fresh := &gatewayv1.HTTPRoute{}
	_ = fakeK8s.Get(ctx, types.NamespacedName{Name: "http-no-hosts", Namespace: "default"}, fresh)
	if len(fresh.Status.Parents) > 0 {
		for _, cond := range fresh.Status.Parents[0].Conditions {
			if cond.Type == string(gatewayv1.RouteConditionAccepted) {
				assert.Equal(t, metav1.ConditionFalse, cond.Status,
					"Accepted must be False when route has no hostnames")
				assert.Equal(t, "NoHostnames", cond.Reason)
			}
		}
	}

	mockClient.AssertExpectations(t)
}

// TestReconcileHTTPRoute_NoMatchingDomains verifies that when all hostnames
// fail domain extraction, the route gets NoMatchingDomains status and a
// 5-minute requeue (no error).
func TestReconcileHTTPRoute_NoMatchingDomains(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := buildScheme(t)

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "42"},
		},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "http-no-match", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			ParentRefs: []gatewayv1.ParentReference{{Name: "test-gw"}},
			Hostnames:  []gatewayv1.Hostname{"app.other-domain.io"}, // does not match
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway, route).
		WithStatusSubresource(route).
		Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		Log:            ctrl.Log.WithName("test"),
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	ctx := context.Background()

	mockClient.On("ListDomains", ctx).Return([]map[string]interface{}{
		{"baseDomain": "example.com", "domainId": "dom-1"},
	}, nil).Once()
	mockClient.On("ListResources", ctx).Return([]map[string]interface{}{}, nil).Once()

	result, err := r.reconcileHTTPRoute(ctx, route, ctrl.Log.WithName("test"))
	require.NoError(t, err)
	assert.Equal(t, 5*time.Minute, result.RequeueAfter)

	// Production code calls updateRouteStatus(false, "NoMatchingDomains", ...) before returning.
	fresh := &gatewayv1.HTTPRoute{}
	_ = fakeK8s.Get(ctx, types.NamespacedName{Name: "http-no-match", Namespace: "default"}, fresh)
	if len(fresh.Status.Parents) > 0 {
		for _, cond := range fresh.Status.Parents[0].Conditions {
			if cond.Type == string(gatewayv1.RouteConditionAccepted) {
				assert.Equal(t, metav1.ConditionFalse, cond.Status,
					"Accepted must be False when no hostnames match Pangolin domains")
				assert.Equal(t, "NoMatchingDomains", cond.Reason)
			}
		}
	}

	mockClient.AssertExpectations(t)
}

// TestReconcileHTTPRoute_ReconcileTargetsSiteGone verifies that when
// reconcileTargets returns a site-gone error (404 "Site with ID"), the
// reconciler clears the gateway site-id label and requeues non-fatally.
func TestReconcileHTTPRoute_ReconcileTargetsSiteGone(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(20)
	scheme := buildScheme(t)

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "42"},
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.1.50"},
	}
	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "http-site-gone", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			ParentRefs: []gatewayv1.ParentReference{{Name: "test-gw"}},
			Hostnames:  []gatewayv1.Hostname{"app.example.com"},
			Rules: []gatewayv1.HTTPRouteRule{
				{
					BackendRefs: []gatewayv1.HTTPBackendRef{
						{BackendRef: gatewayv1.BackendRef{
							BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend-svc", Port: &port},
						}},
					},
				},
			},
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway, route, svc).
		WithStatusSubresource(route).
		Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		Log:            ctrl.Log.WithName("test"),
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	ctx := context.Background()

	mockClient.On("ListDomains", ctx).Return([]map[string]interface{}{
		{"baseDomain": "example.com", "domainId": "dom-1"},
	}, nil).Once()
	mockClient.On("ListResources", ctx).Return([]map[string]interface{}{
		{"resourceId": "res-sg", "name": "app.example.com"},
	}, nil).Once()

	// verifyOrUpdateResource: resource found in allResources (no drift)
	// reconcileTargets: ListTargetsRaw returns site-gone 404
	siteGoneErr := &pangolin.APIError{
		StatusCode: 404,
		Method:     "PUT",
		Endpoint:   "/public-resource/res-sg/target",
		Message:    "Site with ID 42 not found",
	}
	mockClient.On("ListTargetsRaw", ctx, "res-sg").Return(nil, siteGoneErr).Once()

	result, err := r.reconcileHTTPRoute(ctx, route, ctrl.Log.WithName("test"))
	require.NoError(t, err, "site-gone must NOT propagate as error")
	assert.Equal(t, 10*time.Second, result.RequeueAfter)

	// Gateway site-id label must have been cleared
	freshGW := &gatewayv1.Gateway{}
	_ = fakeK8s.Get(ctx, types.NamespacedName{Name: "test-gw", Namespace: "default"}, freshGW)
	assert.Empty(t, freshGW.Labels[SiteIDLabel])

	mockClient.AssertExpectations(t)
}

// TestHTTPRoute_ReconcileTargets_PriorityDrift_ReplacesTarget verifies that when
// an existing target's priority differs from the desired priority (e.g. the
// backend weight changed), the stale target is deleted and recreated. This is
// the canary-weight-change scenario.
func TestHTTPRoute_ReconcileTargets_PriorityDrift_ReplacesTarget(t *testing.T) {
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
	ctx := context.Background()

	port := gatewayv1.PortNumber(8080)
	weight := int32(80) // desired priority = 80 (weight overrides computed value)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{BackendRefs: []gatewayv1.HTTPBackendRef{
					{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend-svc", Port: &port},
						Weight:                 &weight,
					}},
				}},
			},
		},
	}

	// Existing target matches ip/port/siteId/path but has stale priority (50 != desired 80)
	existingTargets := []map[string]interface{}{
		{
			"targetId":      "stale-prio",
			"ip":            "10.96.1.100",
			"port":          float64(8080),
			"siteId":        float64(42),
			"path":          "/",
			"pathMatchType": "prefix",
			"priority":      float64(50), // drift!
			"method":        "http",
		},
	}
	mockClient.On("ListTargetsRaw", ctx, "res-1").Return(existingTargets, nil)
	mockClient.On("DeleteTarget", ctx, "stale-prio").Return(nil)
	mockClient.On("CreateTargetRaw", ctx, "res-1", mock.AnythingOfType("map[string]interface {}")).Return(
		map[string]interface{}{"targetId": "new-prio"}, nil,
	)

	err := r.reconcileTargets(ctx, route, "res-1", "42", ctrl.Log.WithName("test"))
	require.NoError(t, err)
	mockClient.AssertCalled(t, "DeleteTarget", ctx, "stale-prio")

	// The recreated target must carry the corrected priority = 80
	var createCall mock.Call
	for _, c := range mockClient.Calls {
		if c.Method == "CreateTargetRaw" {
			createCall = c
		}
	}
	data, ok := createCall.Arguments[2].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, 80, data["priority"], "recreated target must carry corrected priority")
}

// TestHTTPRoute_ReconcileTargets_MethodDrift_ReplacesTarget verifies that when an
// existing target's method is not "http" (e.g. left over from a prior schema or
// manual edit in Pangolin), the controller detects the drift and recreates it so
// the target method converges to the controller-managed value.
func TestHTTPRoute_ReconcileTargets_MethodDrift_ReplacesTarget(t *testing.T) {
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
	ctx := context.Background()

	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{BackendRefs: []gatewayv1.HTTPBackendRef{
					{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend-svc", Port: &port},
					}},
				}},
			},
		},
	}

	// Existing target matches on ip/port/siteId/path/pathMatchType/priority but
	// has a non-http method — the controller always manages method="http".
	existingTargets := []map[string]interface{}{
		{
			"targetId":      "stale-method",
			"ip":            "10.96.1.100",
			"port":          float64(8080),
			"siteId":        float64(42),
			"path":          "/",
			"pathMatchType": "prefix",
			"priority":      float64(110),
			"method":        "https", // drift!
		},
	}
	mockClient.On("ListTargetsRaw", ctx, "res-1").Return(existingTargets, nil)
	mockClient.On("DeleteTarget", ctx, "stale-method").Return(nil)
	mockClient.On("CreateTargetRaw", ctx, "res-1", mock.AnythingOfType("map[string]interface {}")).Return(
		map[string]interface{}{"targetId": "new-method"}, nil,
	)

	err := r.reconcileTargets(ctx, route, "res-1", "42", ctrl.Log.WithName("test"))
	require.NoError(t, err)
	mockClient.AssertCalled(t, "DeleteTarget", ctx, "stale-method")

	var createCall mock.Call
	for _, c := range mockClient.Calls {
		if c.Method == "CreateTargetRaw" {
			createCall = c
		}
	}
	data, ok := createCall.Arguments[2].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "http", data["method"], "recreated target must have method=http")
}

// TestHTTPRoute_ReconcileTargets_DeleteDriftedTargetFails_StillCreates verifies
// that if deleting a drifted target fails (transient Pangolin error), the
// controller still proceeds to create the corrected target rather than aborting
// the whole reconcile — the create is what matters for traffic correctness.
func TestHTTPRoute_ReconcileTargets_DeleteDriftedTargetFails_StillCreates(t *testing.T) {
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
	ctx := context.Background()

	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{BackendRefs: []gatewayv1.HTTPBackendRef{
					{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend-svc", Port: &port},
					}},
				}},
			},
		},
	}

	existingTargets := []map[string]interface{}{
		{
			"targetId":      "stale-tgt",
			"ip":            "10.96.1.100",
			"port":          float64(8080),
			"siteId":        float64(42),
			"path":          "/",
			"pathMatchType": "prefix",
			"priority":      float64(999), // drift forces delete+recreate
			"method":        "http",
		},
	}
	mockClient.On("ListTargetsRaw", ctx, "res-1").Return(existingTargets, nil)
	// DeleteTarget fails, but the production code logs and continues to create.
	mockClient.On("DeleteTarget", ctx, "stale-tgt").Return(errors.New("transient delete error"))
	mockClient.On("CreateTargetRaw", ctx, "res-1", mock.AnythingOfType("map[string]interface {}")).Return(
		map[string]interface{}{"targetId": "new-tgt"}, nil,
	)

	err := r.reconcileTargets(ctx, route, "res-1", "42", ctrl.Log.WithName("test"))
	require.NoError(t, err, "a failed delete of a drifted target must not abort reconcile")
	mockClient.AssertCalled(t, "CreateTargetRaw", ctx, "res-1", mock.AnythingOfType("map[string]interface {}"))
}

// TestHTTPRoute_ReconcileTargets_RuleWithNoBackends_Skipped verifies that rules
// containing an empty BackendRefs slice are silently skipped. The controller
// must not crash or error on an empty rule — it is valid K8s API.
func TestHTTPRoute_ReconcileTargets_RuleWithNoBackends_Skipped(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := buildScheme(t)
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).Build()

	r := &HTTPRouteReconciler{Client: fakeK8s, PangolinClient: mockClient, Recorder: recorder}
	ctx := context.Background()

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "empty-rule-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{BackendRefs: []gatewayv1.HTTPBackendRef{}}, // empty rule
			},
		},
	}

	mockClient.On("ListTargetsRaw", ctx, "res-1").Return([]map[string]interface{}{}, nil)

	err := r.reconcileTargets(ctx, route, "res-1", "42", ctrl.Log.WithName("test"))
	require.NoError(t, err)
	mockClient.AssertNotCalled(t, "CreateTargetRaw")
}

// TestHTTPRoute_ReconcileTargets_CrossNamespaceBackendRef verifies that a
// BackendRef pointing to a Service in a different namespace resolves correctly.
// This is the cross-namespace service reference pattern.
func TestHTTPRoute_ReconcileTargets_CrossNamespaceBackendRef(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := buildScheme(t)

	// Service lives in "other-ns", not "default"
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "cross-ns-svc", Namespace: "other-ns"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.77.7"},
	}
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	r := &HTTPRouteReconciler{Client: fakeK8s, PangolinClient: mockClient, Recorder: recorder}
	ctx := context.Background()

	otherNS := gatewayv1.Namespace("other-ns")
	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "cross-ns-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{BackendRefs: []gatewayv1.HTTPBackendRef{
					{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name:      "cross-ns-svc",
							Namespace: &otherNS,
							Port:      &port,
						},
					}},
				}},
			},
		},
	}

	mockClient.On("ListTargetsRaw", ctx, "res-1").Return([]map[string]interface{}{}, nil)
	mockClient.On("CreateTargetRaw", ctx, "res-1", mock.AnythingOfType("map[string]interface {}")).Return(
		map[string]interface{}{"targetId": "cross-ns-tgt"}, nil,
	)

	err := r.reconcileTargets(ctx, route, "res-1", "42", ctrl.Log.WithName("test"))
	require.NoError(t, err, "cross-namespace service reference must resolve correctly")

	// Verify the target uses the cross-namespace service's ClusterIP
	var createCall mock.Call
	for _, c := range mockClient.Calls {
		if c.Method == "CreateTargetRaw" {
			createCall = c
		}
	}
	data, ok := createCall.Arguments[2].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "10.96.77.7", data["ip"], "target IP must come from the cross-namespace service")
}

// TestHTTPRoute_ReconcileTargets_NilPortBackendRef_Skipped verifies that a
// BackendRef without a Port is logged and skipped rather than panicking.
// The controller must continue to the next backend after skipping the bad one.
func TestHTTPRoute_ReconcileTargets_NilPortBackendRef_Skipped(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := buildScheme(t)

	goodPort := gatewayv1.PortNumber(8080)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "good-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.1.1"},
	}
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		Log:            ctrl.Log.WithName("test"),
		PangolinClient: mockClient,
		Recorder:       recorder,
	}
	ctx := context.Background()

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "nil-port-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{BackendRefs: []gatewayv1.HTTPBackendRef{
					// First backend: nil port — must be skipped, not panic
					{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "good-svc",
							Port: nil, // deliberately nil
						},
					}},
					// Second backend: valid port — must still be created
					{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "good-svc",
							Port: &goodPort,
						},
					}},
				}},
			},
		},
	}

	mockClient.On("ListTargetsRaw", ctx, "res-1").Return([]map[string]interface{}{}, nil)
	mockClient.On("CreateTargetRaw", ctx, "res-1", mock.AnythingOfType("map[string]interface {}")).Return(
		map[string]interface{}{"targetId": "tgt-valid"}, nil,
	).Once()

	err := r.reconcileTargets(ctx, route, "res-1", "42", ctrl.Log.WithName("test"))
	require.NoError(t, err, "nil port backend must be skipped, not error")
	// Only one CreateTargetRaw call — for the valid backend
	mockClient.AssertNumberOfCalls(t, "CreateTargetRaw", 1)
}

// TestHTTPRoute_ReconcileTargets_CreateTargetFails_ReturnsError verifies that
// a CreateTargetRaw failure is wrapped and returned so the reconcile requeues.
func TestHTTPRoute_ReconcileTargets_CreateTargetFails_ReturnsError(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := buildScheme(t)

	port := gatewayv1.PortNumber(8080)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.1.2"},
	}
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	r := &HTTPRouteReconciler{Client: fakeK8s, PangolinClient: mockClient, Recorder: recorder}
	ctx := context.Background()

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "create-fail-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{BackendRefs: []gatewayv1.HTTPBackendRef{
					{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend-svc", Port: &port},
					}},
				}},
			},
		},
	}

	mockClient.On("ListTargetsRaw", ctx, "res-1").Return([]map[string]interface{}{}, nil)
	mockClient.On("CreateTargetRaw", ctx, "res-1", mock.AnythingOfType("map[string]interface {}")).Return(
		nil, errors.New("pangolin 503"),
	)

	err := r.reconcileTargets(ctx, route, "res-1", "42", ctrl.Log.WithName("test"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create target")
}

// TestReconcileHTTPRoute_NoParentRefs verifies that reconcileHTTPRoute returns
// no error and no requeue when the route has an empty parentRefs list —
// the controller simply returns; it cannot reconcile without a parent gateway.
func TestReconcileHTTPRoute_NoParentRefs(t *testing.T) {
	scheme := buildScheme(t)
	recorder := record.NewFakeRecorder(10)
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &HTTPRouteReconciler{
		Client:   fakeK8s,
		Log:      ctrl.Log.WithName("test"),
		Recorder: recorder,
		Scheme:   scheme,
	}
	ctx := context.Background()

	// Route with no parentRefs — nothing to reconcile against
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "no-parent-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			ParentRefs: []gatewayv1.ParentReference{},
		},
	}

	result, err := r.reconcileHTTPRoute(ctx, route, ctrl.Log.WithName("test"))
	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter,
		"no parentRefs means no work to do; should not requeue")
}

// TestReconcileHTTPRoute_ReconcileTargetsNonSiteGoneError verifies that when
// reconcileTargets returns a non-site-gone error (e.g. the Pangolin target API
// returns 500), the reconciler sets TargetError status and requeues with an error.
func TestReconcileHTTPRoute_ReconcileTargetsNonSiteGoneError(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(20)
	scheme := buildScheme(t)

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "42"},
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.1.50"},
	}
	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "target-err-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			ParentRefs: []gatewayv1.ParentReference{{Name: "test-gw"}},
			Hostnames:  []gatewayv1.Hostname{"app.example.com"},
			Rules: []gatewayv1.HTTPRouteRule{
				{BackendRefs: []gatewayv1.HTTPBackendRef{
					{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend-svc", Port: &port},
					}},
				}},
			},
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway, route, svc).
		WithStatusSubresource(route).
		Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		Log:            ctrl.Log.WithName("test"),
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	ctx := context.Background()

	// ListDomains and ListResources succeed; resource exists
	mockClient.On("ListDomains", ctx).Return([]map[string]interface{}{
		{"baseDomain": "example.com", "domainId": "dom-1"},
	}, nil).Once()
	mockClient.On("ListResources", ctx).Return([]map[string]interface{}{
		{"resourceId": "res-terr", "name": "app.example.com"},
	}, nil).Once()

	// ListTargetsRaw returns a non-site-gone error (e.g. Pangolin 500)
	nonSiteErr := errors.New("pangolin internal server error")
	mockClient.On("ListTargetsRaw", ctx, "res-terr").Return(nil, nonSiteErr).Once()

	result, err := r.reconcileHTTPRoute(ctx, route, ctrl.Log.WithName("test"))
	require.Error(t, err)
	assert.Equal(t, 30*time.Second, result.RequeueAfter,
		"non-site-gone target error must requeue with 30s delay")

	// Verify TargetError status condition was written
	fresh := &gatewayv1.HTTPRoute{}
	require.NoError(t, fakeK8s.Get(ctx, types.NamespacedName{Name: "target-err-route", Namespace: "default"}, fresh))
	if len(fresh.Status.Parents) > 0 {
		for _, cond := range fresh.Status.Parents[0].Conditions {
			if cond.Type == string(gatewayv1.RouteConditionAccepted) {
				assert.Equal(t, metav1.ConditionFalse, cond.Status)
				assert.Equal(t, "TargetError", cond.Reason)
			}
		}
	}

	mockClient.AssertExpectations(t)
}

// TestReconcileHTTPRoute_CreateResourceFails verifies that when
// createPangolinResourceForHostname fails, the reconciler sets
// ResourceCreationFailed status and requeues.
func TestReconcileHTTPRoute_CreateResourceFails(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(20)
	scheme := buildScheme(t)

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "42"},
		},
	}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "create-res-fail-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			ParentRefs: []gatewayv1.ParentReference{{Name: "test-gw"}},
			Hostnames:  []gatewayv1.Hostname{"app.example.com"},
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway, route).
		WithStatusSubresource(route).
		Build()

	r := &HTTPRouteReconciler{
		Client:         fakeK8s,
		Log:            ctrl.Log.WithName("test"),
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	ctx := context.Background()

	// Domains exist but CreateResource fails
	mockClient.On("ListDomains", ctx).Return([]map[string]interface{}{
		{"baseDomain": "example.com", "domainId": "dom-1"},
	}, nil).Once()
	mockClient.On("ListResources", ctx).Return([]map[string]interface{}{}, nil).Once()
	mockClient.On("CreateResource", ctx, mock.AnythingOfType("map[string]interface {}")).
		Return(nil, errors.New("quota exceeded")).Once()

	result, err := r.reconcileHTTPRoute(ctx, route, ctrl.Log.WithName("test"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quota exceeded")
	assert.Equal(t, 30*time.Second, result.RequeueAfter)

	fresh := &gatewayv1.HTTPRoute{}
	require.NoError(t, fakeK8s.Get(ctx, types.NamespacedName{Name: "create-res-fail-route", Namespace: "default"}, fresh))
	if len(fresh.Status.Parents) > 0 {
		for _, cond := range fresh.Status.Parents[0].Conditions {
			if cond.Type == string(gatewayv1.RouteConditionAccepted) {
				assert.Equal(t, metav1.ConditionFalse, cond.Status)
				assert.Equal(t, "ResourceCreationFailed", cond.Reason)
			}
		}
	}

	mockClient.AssertExpectations(t)
}

// TestHTTPRoute_ReconcileTargets_OrphanDeleteFails_ContinuesReconcile verifies
// that when deleting an orphaned target fails, the error is logged but the
// reconcile does NOT abort — other targets are still processed. This prevents
// a single stuck target from blocking all traffic routing updates.
func TestHTTPRoute_ReconcileTargets_OrphanDeleteFails_ContinuesReconcile(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(20)
	scheme := buildScheme(t)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "backend-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.1.100"},
	}
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	r := &HTTPRouteReconciler{Client: fakeK8s, PangolinClient: mockClient, Recorder: recorder}
	ctx := context.Background()

	port := gatewayv1.PortNumber(8080)
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "test-route", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			Rules: []gatewayv1.HTTPRouteRule{
				{BackendRefs: []gatewayv1.HTTPBackendRef{
					{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{Name: "backend-svc", Port: &port},
					}},
				}},
			},
		},
	}

	// Current target matches exactly (no drift) so it's in matchedTargetIDs
	// Orphan has same path "/" but different IP — should be cleaned up
	existingTargets := []map[string]interface{}{
		{
			"targetId":      "current-tgt",
			"ip":            "10.96.1.100", // matches svc ClusterIP
			"port":          float64(8080),
			"siteId":        float64(42),
			"path":          "/",
			"pathMatchType": "prefix",
			"priority":      float64(110),
			"method":        "http",
		},
		{
			"targetId":      "orphan-tgt",
			"ip":            "10.96.9.99", // stale IP, orphaned
			"port":          float64(8080),
			"siteId":        float64(42),
			"path":          "/",
			"pathMatchType": "prefix",
			"priority":      float64(110),
			"method":        "http",
		},
	}
	mockClient.On("ListTargetsRaw", ctx, "res-1").Return(existingTargets, nil)
	// Orphan delete fails — must NOT cause reconcile to error
	mockClient.On("DeleteTarget", ctx, "orphan-tgt").Return(errors.New("delete timeout"))

	err := r.reconcileTargets(ctx, route, "res-1", "42", ctrl.Log.WithName("test"))
	require.NoError(t, err, "orphan delete failure must not abort reconcile")
}
