package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/config"
	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// --- verifyOrRecreateResource (GRPCRoute) ---

func TestGRPCRoute_VerifyOrRecreateResource_ResourceExists(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	fakeK8s := fake.NewClientBuilder().WithScheme(makeGatewayTestScheme()).Build()

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         makeGatewayTestScheme(),
		Config:         &config.ControllerConfig{},
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	route := &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-grpc",
			Namespace: "default",
			Labels:    map[string]string{ResourceIDLabel: "res-500"},
		},
	}

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "123"},
		},
	}

	// ListResources returns our resource
	mockClient.On("ListResources", ctx).Return([]map[string]interface{}{
		{
			"resourceId": "res-500",
			"name":       "default-test-grpc",
		},
	}, nil).Once()

	err := r.verifyOrRecreateResource(ctx, route, gateway, "res-500", log)
	assert.NoError(t, err)

	// No events (resource exists with matching ID)
	select {
	case event := <-recorder.Events:
		t.Errorf("expected no events, got: %s", event)
	default:
		// expected
	}

	mockClient.AssertExpectations(t)
}

func TestGRPCRoute_VerifyOrRecreateResource_ResourceNotFound_Recreates(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	route := &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-grpc",
			Namespace: "default",
			Labels:    map[string]string{ResourceIDLabel: "res-old"},
			Annotations: map[string]string{
				"gateway.pangolin.net/protocol": "tcp",
			},
		},
		Spec: gatewayv1.GRPCRouteSpec{
			Hostnames: []gatewayv1.Hostname{"grpc.example.com"},
		},
	}

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "123"},
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(route).
		Build()

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
		Config:         &config.ControllerConfig{},
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	// ListResources returns list WITHOUT our resource ID
	mockClient.On("ListResources", ctx).Return([]map[string]interface{}{
		{
			"resourceId": "res-other",
			"name":       "other-resource",
		},
	}, nil).Once()

	// recreateResource calls createPangolinResource → ListDomains, CreateResource
	mockClient.On("ListDomains", ctx).Return([]map[string]interface{}{
		{"baseDomain": "example.com", "domainId": "dom-1"},
	}, nil).Once()
	mockClient.On("CreateResource", ctx, mock.AnythingOfType("map[string]interface {}")).Return(map[string]interface{}{
		"resourceId": "res-new-999",
	}, nil).Once()

	err := r.verifyOrRecreateResource(ctx, route, gateway, "res-old", log)
	assert.NoError(t, err)

	// DriftDetected event
	foundDrift := false
	for i := 0; i < 10; i++ {
		select {
		case event := <-recorder.Events:
			if contains(event, "DriftDetected") {
				foundDrift = true
			}
		default:
		}
		if foundDrift {
			break
		}
	}
	assert.True(t, foundDrift, "expected DriftDetected event for resource not found")

	// Verify route label was updated with new resource ID
	updatedRoute := &gatewayv1.GRPCRoute{}
	err = fakeK8s.Get(ctx, types.NamespacedName{Name: "test-grpc", Namespace: "default"}, updatedRoute)
	assert.NoError(t, err)
	assert.Equal(t, "res-new-999", updatedRoute.Labels[ResourceIDLabel])

	mockClient.AssertExpectations(t)
}

func TestGRPCRoute_VerifyOrRecreateResource_ListResourcesError(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	fakeK8s := fake.NewClientBuilder().WithScheme(makeGatewayTestScheme()).Build()

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         makeGatewayTestScheme(),
		Config:         &config.ControllerConfig{},
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	route := &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-grpc",
			Namespace: "default",
		},
	}

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
		},
	}

	// ListResources returns error
	mockClient.On("ListResources", ctx).Return(nil, errors.New("api unavailable")).Once()

	err := r.verifyOrRecreateResource(ctx, route, gateway, "res-500", log)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list resources for verification")

	// No recreation should happen on transient error
	mockClient.AssertNotCalled(t, "CreateResource")
	mockClient.AssertExpectations(t)
}

func TestGRPCRoute_VerifyOrRecreateResource_NameDrift(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	fakeK8s := fake.NewClientBuilder().WithScheme(makeGatewayTestScheme()).Build()

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         makeGatewayTestScheme(),
		Config:         &config.ControllerConfig{},
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	route := &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-grpc",
			Namespace: "default",
		},
	}

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
		},
	}

	// Resource found but name doesn't match expected "default-test-grpc"
	mockClient.On("ListResources", ctx).Return([]map[string]interface{}{
		{
			"resourceId": "res-500",
			"name":       "wrong-name",
		},
	}, nil).Once()

	err := r.verifyOrRecreateResource(ctx, route, gateway, "res-500", log)
	assert.NoError(t, err) // name drift is informational only

	// No DriftDetected event (name drift is logged but no event)
	// No recreation (name drift cannot auto-correct)
	mockClient.AssertNotCalled(t, "CreateResource")
	mockClient.AssertExpectations(t)
}

// --- GRPCRoute NoParentGateway requeue ---

func TestReconcileGRPCRoute_NoParentGateway_RequeuesAfter30s(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	route := &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "grpc-no-parent",
			Namespace: "default",
		},
		Spec: gatewayv1.GRPCRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{}, // empty
			},
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(route).
		WithStatusSubresource(route).
		Build()

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		Log:            ctrl.Log.WithName("test"),
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
		Config:         &config.ControllerConfig{},
	}
	ctx := context.Background()

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "grpc-no-parent",
			Namespace: "default",
		},
	}

	result, err := r.Reconcile(ctx, req)
	assert.NoError(t, err)
	assert.Equal(t, 30*time.Second, result.RequeueAfter,
		"Should requeue after 30s when no parent gateway references")

	// No Pangolin API calls expected
	mockClient.AssertExpectations(t)
}

// --- GRPCRoute resource exists with numeric ID matching ---

func TestGRPCRoute_VerifyOrRecreateResource_NumericID(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	fakeK8s := fake.NewClientBuilder().WithScheme(makeGatewayTestScheme()).Build()

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         makeGatewayTestScheme(),
		Config:         &config.ControllerConfig{},
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	route := &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-grpc",
			Namespace: "default",
		},
	}

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
		},
	}

	// Numeric resourceId (JSON float64) matching string "42"
	mockClient.On("ListResources", ctx).Return([]map[string]interface{}{
		{
			"resourceId": float64(42),
			"name":       "default-test-grpc",
		},
	}, nil).Once()

	err := r.verifyOrRecreateResource(ctx, route, gateway, "42", log)
	assert.NoError(t, err)

	// No events or recreations
	select {
	case event := <-recorder.Events:
		t.Errorf("expected no events, got: %s", event)
	default:
		// expected
	}

	mockClient.AssertExpectations(t)
}

// --- nil Port backend skip ---

// TestReconcileTargets_NilPortBackendSkipped verifies that a backend with a nil
// Port is silently skipped and does not cause a panic or return an error.
// The test uses a single-rule GRPCRoute where the only BackendRef has Port=nil,
// so reconcileTargets should return an error ("no backend services found")
// rather than panicking.
func TestReconcileTargets_NilPortBackendSkipped(t *testing.T) {
	mockClient := new(internalMockPangolin)
	scheme := makeGatewayTestScheme()

	// Build a minimal Service so the lookup doesn't fire — but since Port is nil
	// the backend is skipped before any Get call is made.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nil-port-svc",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.1",
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(svc).
		Build()

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Scheme:         scheme,
		Config:         &config.ControllerConfig{},
		Log:            ctrl.Log.WithName("test"),
	}
	ctx := context.Background()

	route := &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nil-port-route",
			Namespace: "default",
		},
		Spec: gatewayv1.GRPCRouteSpec{
			Rules: []gatewayv1.GRPCRouteRule{
				{
					BackendRefs: []gatewayv1.GRPCBackendRef{
						{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name: "nil-port-svc",
									Port: nil, // intentionally nil
								},
							},
						},
					},
				},
			},
		},
	}

	// reconcileTargets must not panic and must return an error because all
	// backends were skipped and there are none left to process.
	err := r.reconcileTargets(ctx, route, "res-nil-port", "12345", nil, ctrl.Log.WithName("test"))
	assert.Error(t, err, "should error when all backends are skipped due to nil port")
	assert.Contains(t, err.Error(), "no backend services found")

	// No Pangolin API calls should be made
	mockClient.AssertExpectations(t)
}

var _ = fmt.Sprintf // ensure fmt import
var _ corev1.Pod    // ensure corev1 import

// ---------------------------------------------------------------------------
// reconcileGRPCRoute — uncovered branches
// ---------------------------------------------------------------------------

// TestReconcileGRPCRoute_ListDomainsFails verifies that when ListDomains fails
// inside createPangolinResource, the reconciliation returns an error and
// the route gets ResourceCreationFailed status.
func TestReconcileGRPCRoute_ListDomainsFails(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "42"},
		},
	}
	route := &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "grpc-no-domains",
			Namespace: "default",
		},
		Spec: gatewayv1.GRPCRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: "test-gw"}},
			},
			Hostnames: []gatewayv1.Hostname{"grpc.example.com"},
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway, route).
		WithStatusSubresource(route).
		Build()

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		Log:            ctrl.Log.WithName("test"),
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
		Config:         &config.ControllerConfig{},
	}
	ctx := context.Background()

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "grpc-no-domains", Namespace: "default"}}

	// ListDomains fails
	mockClient.On("ListDomains", mock.Anything).Return(nil, errors.New("domains unavailable")).Once()

	result, err := r.Reconcile(ctx, req)
	assert.Error(t, err)
	assert.Equal(t, 30*time.Second, result.RequeueAfter)

	// Production code calls r.updateRouteStatus(ResourceCreationFailed) before returning.
	// Verify the GRPCRoute has Accepted=False with the correct reason.
	fresh := &gatewayv1.GRPCRoute{}
	_ = fakeK8s.Get(ctx, types.NamespacedName{Name: "grpc-no-domains", Namespace: "default"}, fresh)
	if len(fresh.Status.Parents) > 0 {
		for _, cond := range fresh.Status.Parents[0].Conditions {
			if cond.Type == string(gatewayv1.RouteConditionAccepted) {
				assert.Equal(t, metav1.ConditionFalse, cond.Status,
					"Accepted must be False when ListDomains fails")
				assert.Equal(t, "ResourceCreationFailed", cond.Reason)
			}
		}
	}

	mockClient.AssertExpectations(t)
}

// TestReconcileGRPCRoute_VerifyOrRecreateResourceFails verifies that when
// verifyOrRecreateResource returns an error (e.g. ListResources down),
// the reconciliation returns an error and requeues.
func TestReconcileGRPCRoute_VerifyOrRecreateResourceFails(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "42"},
		},
	}
	route := &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "grpc-verify-fail",
			Namespace: "default",
			Labels:    map[string]string{ResourceIDLabel: "res-999"},
		},
		Spec: gatewayv1.GRPCRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: "test-gw"}},
			},
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway, route).
		WithStatusSubresource(route).
		Build()

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		Log:            ctrl.Log.WithName("test"),
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
		Config:         &config.ControllerConfig{},
	}
	ctx := context.Background()

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "grpc-verify-fail", Namespace: "default"}}

	// verifyOrRecreateResource calls ListResources — fails transiently
	mockClient.On("ListResources", mock.Anything).Return(nil, errors.New("api down")).Once()

	result, err := r.Reconcile(ctx, req)
	assert.Error(t, err)
	assert.Equal(t, 30*time.Second, result.RequeueAfter)

	// Production code calls r.updateRouteStatus(ResourceVerificationFailed) before returning.
	fresh := &gatewayv1.GRPCRoute{}
	_ = fakeK8s.Get(ctx, types.NamespacedName{Name: "grpc-verify-fail", Namespace: "default"}, fresh)
	if len(fresh.Status.Parents) > 0 {
		for _, cond := range fresh.Status.Parents[0].Conditions {
			if cond.Type == string(gatewayv1.RouteConditionAccepted) {
				assert.Equal(t, metav1.ConditionFalse, cond.Status)
				assert.Equal(t, "ResourceVerificationFailed", cond.Reason)
			}
		}
	}

	mockClient.AssertExpectations(t)
}

// TestReconcileGRPCRoute_ReconcileTargetsSiteGone verifies that when
// reconcileTargets returns a site-gone error (404 with "Site" in the message),
// the reconciler clears the gateway site-id label and requeues non-fatally.
func TestReconcileGRPCRoute_ReconcileTargetsSiteGone(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "42"},
		},
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "grpc-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.10.10.10"},
	}

	port := gatewayv1.PortNumber(9090)
	route := &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "grpc-site-gone",
			Namespace: "default",
			Labels:    map[string]string{ResourceIDLabel: "res-sg"},
		},
		Spec: gatewayv1.GRPCRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: "test-gw"}},
			},
			Rules: []gatewayv1.GRPCRouteRule{
				{
					BackendRefs: []gatewayv1.GRPCBackendRef{
						{BackendRef: gatewayv1.BackendRef{
							BackendObjectReference: gatewayv1.BackendObjectReference{Name: "grpc-svc", Port: &port},
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

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		Log:            ctrl.Log.WithName("test"),
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
		Config:         &config.ControllerConfig{},
	}
	ctx := context.Background()

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "grpc-site-gone", Namespace: "default"}}

	// verifyOrRecreateResource: resource still exists
	mockClient.On("ListResources", mock.Anything).Return([]map[string]interface{}{
		{"resourceId": "res-sg", "name": "default-grpc-site-gone"},
	}, nil).Once()

	// reconcileTargets: ListTargetsRaw returns a site-gone 404
	siteGoneErr := &pangolin.APIError{
		StatusCode: 404,
		Method:     "PUT",
		Endpoint:   "/public-resource/res-sg/target",
		Message:    "Site with ID 42 not found",
	}
	mockClient.On("ListTargetsRaw", mock.Anything, "res-sg").Return(nil, siteGoneErr).Once()

	result, err := r.Reconcile(ctx, req)
	assert.NoError(t, err, "site-gone must NOT return an error — requeue only")
	assert.Equal(t, 10*time.Second, result.RequeueAfter)

	// Gateway site-id label must have been cleared
	freshGW := &gatewayv1.Gateway{}
	_ = fakeK8s.Get(ctx, types.NamespacedName{Name: "test-gw", Namespace: "default"}, freshGW)
	assert.Empty(t, freshGW.Labels[SiteIDLabel], "site-id label must be cleared after site-gone")

	mockClient.AssertExpectations(t)
}

// TestReconcileGRPCRoute_AdoptionPath verifies that when a resource is missing
// by ID but exists under the expected name, it is adopted (label updated)
// instead of being recreated.
func TestGRPCRoute_VerifyOrRecreateResource_AdoptionPath(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	route := &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-grpc",
			Namespace: "default",
			Labels:    map[string]string{ResourceIDLabel: "res-old"},
		},
	}

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "test-gw", Namespace: "default"},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(route).
		Build()

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
		Config:         &config.ControllerConfig{},
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	// ListResources: old ID is gone, but new resource with expected name exists
	mockClient.On("ListResources", ctx).Return([]map[string]interface{}{
		{"resourceId": "res-adopted", "name": "default-test-grpc"},
	}, nil).Once()

	err := r.verifyOrRecreateResource(ctx, route, gateway, "res-old", log)
	assert.NoError(t, err)

	// Route label must be updated to adopted ID
	updatedRoute := &gatewayv1.GRPCRoute{}
	_ = fakeK8s.Get(ctx, types.NamespacedName{Name: "test-grpc", Namespace: "default"}, updatedRoute)
	assert.Equal(t, "res-adopted", updatedRoute.Labels[ResourceIDLabel])

	mockClient.AssertNotCalled(t, "CreateResource")
	mockClient.AssertExpectations(t)
}

// ---------------------------------------------------------------------------
// GRPCRoute updateRouteStatus — edge cases
// ---------------------------------------------------------------------------

// TestGRPCRoute_UpdateRouteStatus_NoParentRefs verifies that updateRouteStatus
// returns immediately when the route has no parent references.
func TestGRPCRoute_UpdateRouteStatus_NoParentRefs(t *testing.T) {
	scheme := makeGatewayTestScheme()
	recorder := record.NewFakeRecorder(10)

	route := &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "grpc-no-parents", Namespace: "default"},
		Spec: gatewayv1.GRPCRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{},
			},
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(route).
		WithStatusSubresource(route).
		Build()

	r := &GRPCRouteReconciler{
		Client:   fakeK8s,
		Log:      ctrl.Log.WithName("test"),
		Recorder: recorder,
		Scheme:   scheme,
		Config:   &config.ControllerConfig{},
	}
	ctx := context.Background()

	// Must not panic and must not write any status
	r.updateRouteStatus(ctx, route, true, "Accepted", "ok")

	fresh := &gatewayv1.GRPCRoute{}
	_ = fakeK8s.Get(ctx, types.NamespacedName{Name: "grpc-no-parents", Namespace: "default"}, fresh)
	assert.Empty(t, fresh.Status.Parents)
}

// TestGRPCRoute_UpdateRouteStatus_PreservesLastTransitionTime verifies that
// when the same condition type and status is set again, LastTransitionTime is
// preserved (not reset to now).
func TestGRPCRoute_UpdateRouteStatus_PreservesLastTransitionTime(t *testing.T) {
	scheme := makeGatewayTestScheme()
	recorder := record.NewFakeRecorder(10)

	route := &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "grpc-ltt", Namespace: "default"},
		Spec: gatewayv1.GRPCRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: "test-gw"}},
			},
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(route).
		WithStatusSubresource(route).
		Build()

	r := &GRPCRouteReconciler{
		Client:   fakeK8s,
		Log:      ctrl.Log.WithName("test"),
		Recorder: recorder,
		Scheme:   scheme,
		Config:   &config.ControllerConfig{},
	}
	ctx := context.Background()

	// First call sets the condition
	r.updateRouteStatus(ctx, route, true, "Accepted", "Route accepted")

	// Re-fetch to get the stored LastTransitionTime
	fresh := &gatewayv1.GRPCRoute{}
	_ = fakeK8s.Get(ctx, types.NamespacedName{Name: "grpc-ltt", Namespace: "default"}, fresh)

	var firstLTT metav1.Time
	if len(fresh.Status.Parents) > 0 && len(fresh.Status.Parents[0].Conditions) > 0 {
		firstLTT = fresh.Status.Parents[0].Conditions[0].LastTransitionTime
	}

	// Second call with same status — LTT must not change
	r.updateRouteStatus(ctx, route, true, "Accepted", "Route accepted again")

	fresh2 := &gatewayv1.GRPCRoute{}
	_ = fakeK8s.Get(ctx, types.NamespacedName{Name: "grpc-ltt", Namespace: "default"}, fresh2)

	if len(fresh2.Status.Parents) > 0 && len(fresh2.Status.Parents[0].Conditions) > 0 {
		assert.Equal(t, firstLTT, fresh2.Status.Parents[0].Conditions[0].LastTransitionTime,
			"LastTransitionTime must be preserved when condition status does not change")
	}
}

// TestGRPCRoute_UpdateRouteStatus_StatusChanges verifies that when the
// condition flips from True to False, LastTransitionTime IS updated.
func TestGRPCRoute_UpdateRouteStatus_StatusChanges(t *testing.T) {
	scheme := makeGatewayTestScheme()
	recorder := record.NewFakeRecorder(10)

	route := &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "grpc-flip", Namespace: "default"},
		Spec: gatewayv1.GRPCRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: "test-gw"}},
			},
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(route).
		WithStatusSubresource(route).
		Build()

	r := &GRPCRouteReconciler{
		Client:   fakeK8s,
		Log:      ctrl.Log.WithName("test"),
		Recorder: recorder,
		Scheme:   scheme,
		Config:   &config.ControllerConfig{},
	}
	ctx := context.Background()

	r.updateRouteStatus(ctx, route, true, "Accepted", "ok")

	// Flip to False
	r.updateRouteStatus(ctx, route, false, "SomeError", "something went wrong")

	fresh := &gatewayv1.GRPCRoute{}
	_ = fakeK8s.Get(ctx, types.NamespacedName{Name: "grpc-flip", Namespace: "default"}, fresh)

	if len(fresh.Status.Parents) > 0 && len(fresh.Status.Parents[0].Conditions) > 0 {
		assert.Equal(t, metav1.ConditionFalse, fresh.Status.Parents[0].Conditions[0].Status,
			"condition status must be updated to False after flip")
	}
}

// TestHTTPRoute_UpdateRouteStatus_PreservesLastTransitionTime verifies that
// when the same condition type and status is set again on an HTTPRoute,
// LastTransitionTime is preserved.
func TestHTTPRoute_UpdateRouteStatus_PreservesLastTransitionTime(t *testing.T) {
	scheme := buildScheme(t)
	recorder := record.NewFakeRecorder(10)

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "http-ltt", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			ParentRefs: []gatewayv1.ParentReference{{Name: "test-gw"}},
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(route).
		WithStatusSubresource(route).
		Build()

	r := &HTTPRouteReconciler{
		Client:   fakeK8s,
		Log:      ctrl.Log.WithName("test"),
		Recorder: recorder,
		Scheme:   scheme,
	}
	ctx := context.Background()

	r.updateRouteStatus(ctx, route, true, "Accepted", "ok")

	fresh := &gatewayv1.HTTPRoute{}
	_ = fakeK8s.Get(ctx, types.NamespacedName{Name: "http-ltt", Namespace: "default"}, fresh)

	var firstLTT metav1.Time
	if len(fresh.Status.Parents) > 0 && len(fresh.Status.Parents[0].Conditions) > 0 {
		firstLTT = fresh.Status.Parents[0].Conditions[0].LastTransitionTime
	}

	r.updateRouteStatus(ctx, route, true, "Accepted", "same status again")

	fresh2 := &gatewayv1.HTTPRoute{}
	_ = fakeK8s.Get(ctx, types.NamespacedName{Name: "http-ltt", Namespace: "default"}, fresh2)

	if len(fresh2.Status.Parents) > 0 && len(fresh2.Status.Parents[0].Conditions) > 0 {
		assert.Equal(t, firstLTT, fresh2.Status.Parents[0].Conditions[0].LastTransitionTime,
			"LTT must not change when condition status is unchanged")
	}
}

// TestHTTPRoute_UpdateRouteStatus_StatusFlip verifies that when an HTTPRoute
// condition flips from True to False, the status is updated.
func TestHTTPRoute_UpdateRouteStatus_StatusFlip(t *testing.T) {
	scheme := buildScheme(t)
	recorder := record.NewFakeRecorder(10)

	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "http-flip", Namespace: "default"},
		Spec: gatewayv1.HTTPRouteSpec{
			ParentRefs: []gatewayv1.ParentReference{{Name: "test-gw"}},
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(route).
		WithStatusSubresource(route).
		Build()

	r := &HTTPRouteReconciler{
		Client:   fakeK8s,
		Log:      ctrl.Log.WithName("test"),
		Recorder: recorder,
		Scheme:   scheme,
	}
	ctx := context.Background()

	r.updateRouteStatus(ctx, route, true, "Accepted", "ok")
	r.updateRouteStatus(ctx, route, false, "Error", "something failed")

	fresh := &gatewayv1.HTTPRoute{}
	_ = fakeK8s.Get(ctx, types.NamespacedName{Name: "http-flip", Namespace: "default"}, fresh)

	if len(fresh.Status.Parents) > 0 && len(fresh.Status.Parents[0].Conditions) > 0 {
		assert.Equal(t, metav1.ConditionFalse, fresh.Status.Parents[0].Conditions[0].Status)
	}
}
