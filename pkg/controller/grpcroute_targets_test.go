package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/dxas90/pangolin-gateway-controller/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// grpcBackend builds a GRPCRoute with a single rule referencing one backend
// service:port. gateway is the parent used only for the gateway argument.
func grpcRouteWithBackend(name, ns, svcName string, port int32) *gatewayv1.GRPCRoute {
	p := gatewayv1.PortNumber(port)
	return &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: gatewayv1.GRPCRouteSpec{
			Rules: []gatewayv1.GRPCRouteRule{
				{
					BackendRefs: []gatewayv1.GRPCBackendRef{
						{BackendRef: gatewayv1.BackendRef{
							BackendObjectReference: gatewayv1.BackendObjectReference{
								Name: gatewayv1.ObjectName(svcName),
								Port: &p,
							},
						}},
					},
				},
			},
		},
	}
}

// TestGRPCRoute_ReconcileTargets_CreatesTarget verifies that a backend with a
// resolvable ClusterIP produces exactly one CreateTargetRaw call carrying the
// correct siteId, ip and port.
func TestGRPCRoute_ReconcileTargets_CreatesTarget(t *testing.T) {
	mockClient := new(internalMockPangolin)
	scheme := makeGatewayTestScheme()

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "grpc-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.5.5"},
	}
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       record.NewFakeRecorder(20),
		Scheme:         scheme,
		Config:         &config.ControllerConfig{},
	}
	ctx := context.Background()
	route := grpcRouteWithBackend("grpc-route", "default", "grpc-svc", 9090)

	mockClient.On("ListTargetsRaw", ctx, "res-1").Return([]map[string]interface{}{}, nil)
	mockClient.On("CreateTargetRaw", ctx, "res-1", mock.AnythingOfType("map[string]interface {}")).Return(
		map[string]interface{}{"targetId": float64(1)}, nil,
	)

	err := r.reconcileTargets(ctx, route, "res-1", "42", nil, ctrl.Log.WithName("test"))
	require.NoError(t, err)

	// Verify the created target payload carries the correct values
	var createCall mock.Call
	for _, c := range mockClient.Calls {
		if c.Method == "CreateTargetRaw" {
			createCall = c
		}
	}
	data, ok := createCall.Arguments[2].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "10.96.5.5", data["ip"])
	assert.Equal(t, 9090, data["port"])
	assert.Equal(t, 42, data["siteId"])
	assert.Equal(t, true, data["enabled"])
}

// TestGRPCRoute_ReconcileTargets_IdempotentSkip verifies that when a target with
// the same ip:port:siteId already exists, no new target is created. This is the
// steady-state reconcile case that must NOT thrash the Pangolin API.
func TestGRPCRoute_ReconcileTargets_IdempotentSkip(t *testing.T) {
	mockClient := new(internalMockPangolin)
	scheme := makeGatewayTestScheme()

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "grpc-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.5.5"},
	}
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       record.NewFakeRecorder(20),
		Scheme:         scheme,
		Config:         &config.ControllerConfig{},
	}
	ctx := context.Background()
	route := grpcRouteWithBackend("grpc-route", "default", "grpc-svc", 9090)

	// Existing target matches exactly — ip, port, siteId all equal.
	existing := []map[string]interface{}{
		{"targetId": float64(7), "ip": "10.96.5.5", "port": float64(9090), "siteId": float64(42)},
	}
	mockClient.On("ListTargetsRaw", ctx, "res-1").Return(existing, nil)

	err := r.reconcileTargets(ctx, route, "res-1", "42", nil, ctrl.Log.WithName("test"))
	require.NoError(t, err)

	mockClient.AssertNotCalled(t, "CreateTargetRaw")
}

// TestGRPCRoute_ReconcileTargets_CreateError verifies that a CreateTargetRaw
// failure is wrapped and propagated so the reconcile requeues.
func TestGRPCRoute_ReconcileTargets_CreateError(t *testing.T) {
	mockClient := new(internalMockPangolin)
	scheme := makeGatewayTestScheme()

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "grpc-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.5.5"},
	}
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       record.NewFakeRecorder(20),
		Scheme:         scheme,
		Config:         &config.ControllerConfig{},
	}
	ctx := context.Background()
	route := grpcRouteWithBackend("grpc-route", "default", "grpc-svc", 9090)

	mockClient.On("ListTargetsRaw", ctx, "res-1").Return([]map[string]interface{}{}, nil)
	mockClient.On("CreateTargetRaw", ctx, "res-1", mock.AnythingOfType("map[string]interface {}")).Return(
		nil, errors.New("pangolin 500"),
	)

	err := r.reconcileTargets(ctx, route, "res-1", "42", nil, ctrl.Log.WithName("test"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create target")
}

// TestGRPCRoute_ReconcileTargets_InvalidSiteID verifies a non-numeric site ID is
// rejected before any target work.
func TestGRPCRoute_ReconcileTargets_InvalidSiteID(t *testing.T) {
	mockClient := new(internalMockPangolin)
	scheme := makeGatewayTestScheme()

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "grpc-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.5.5"},
	}
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       record.NewFakeRecorder(20),
		Scheme:         scheme,
		Config:         &config.ControllerConfig{},
	}
	ctx := context.Background()
	route := grpcRouteWithBackend("grpc-route", "default", "grpc-svc", 9090)

	// ListTargetsRaw is called before the siteID parse in the GRPC implementation.
	mockClient.On("ListTargetsRaw", ctx, "res-1").Return([]map[string]interface{}{}, nil)

	err := r.reconcileTargets(ctx, route, "res-1", "not-a-number", nil, ctrl.Log.WithName("test"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid site ID")
}

// TestGRPCRoute_ReconcileTargets_ServiceNotFound verifies that a backend pointing
// at a non-existent Service fails the reconcile with a clear error.
func TestGRPCRoute_ReconcileTargets_ServiceNotFound(t *testing.T) {
	mockClient := new(internalMockPangolin)
	scheme := makeGatewayTestScheme()
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).Build() // no services

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       record.NewFakeRecorder(20),
		Scheme:         scheme,
		Config:         &config.ControllerConfig{},
	}
	ctx := context.Background()
	route := grpcRouteWithBackend("grpc-route", "default", "missing-svc", 9090)

	mockClient.On("ListTargetsRaw", ctx, "res-1").Return([]map[string]interface{}{}, nil)

	err := r.reconcileTargets(ctx, route, "res-1", "42", nil, ctrl.Log.WithName("test"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get service")
}

// TestGRPCRoute_ReconcileTargets_HeadlessService verifies that a headless Service
// (ClusterIP "None") is rejected — Pangolin needs a routable ClusterIP.
func TestGRPCRoute_ReconcileTargets_HeadlessService(t *testing.T) {
	mockClient := new(internalMockPangolin)
	scheme := makeGatewayTestScheme()

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "headless", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "None"},
	}
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       record.NewFakeRecorder(20),
		Scheme:         scheme,
		Config:         &config.ControllerConfig{},
	}
	ctx := context.Background()
	route := grpcRouteWithBackend("grpc-route", "default", "headless", 9090)

	mockClient.On("ListTargetsRaw", ctx, "res-1").Return([]map[string]interface{}{}, nil)

	err := r.reconcileTargets(ctx, route, "res-1", "42", nil, ctrl.Log.WithName("test"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no ClusterIP")
}

// TestGRPCRoute_ReconcileTargets_ListTargetsError verifies a ListTargetsRaw
// failure is wrapped and propagated.
func TestGRPCRoute_ReconcileTargets_ListTargetsError(t *testing.T) {
	mockClient := new(internalMockPangolin)
	scheme := makeGatewayTestScheme()

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "grpc-svc", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.96.5.5"},
	}
	fakeK8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(svc).Build()

	r := &GRPCRouteReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       record.NewFakeRecorder(20),
		Scheme:         scheme,
		Config:         &config.ControllerConfig{},
	}
	ctx := context.Background()
	route := grpcRouteWithBackend("grpc-route", "default", "grpc-svc", 9090)

	mockClient.On("ListTargetsRaw", ctx, "res-1").Return(nil, errors.New("api down"))

	err := r.reconcileTargets(ctx, route, "res-1", "42", nil, ctrl.Log.WithName("test"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list existing targets")
}
