package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/config"
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

var _ = fmt.Sprintf // ensure fmt import
var _ corev1.Pod    // ensure corev1 import
