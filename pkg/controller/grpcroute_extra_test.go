package controller_test

import (
	"testing"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/config"
	"github.com/dxas90/pangolin-gateway-controller/pkg/controller"
	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/dxas90/pangolin-gateway-controller/pkg/testutil"
	"github.com/stretchr/testify/suite"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// GRPCRouteDriftTestSuite exercises drift-detection and edge-case behaviour of
// the GRPCRouteReconciler using a real envtest API server.
type GRPCRouteDriftTestSuite struct {
	testutil.EnvTestSuite
	reconciler    *controller.GRPCRouteReconciler
	mockPangolin  *testutil.MockPangolinClient
	testNamespace string
}

func (s *GRPCRouteDriftTestSuite) SetupSuite() {
	s.EnvTestSuite.SetupSuite()
	s.testNamespace = "grpcroute-drift-ns"

	ns := testutil.NewTestNamespace(s.testNamespace)
	err := s.Client().Create(s.Context(), ns)
	s.Require().NoError(err)
}

func (s *GRPCRouteDriftTestSuite) SetupTest() {
	s.mockPangolin = testutil.NewMockPangolinClient()

	cfg := &config.ControllerConfig{
		GatewayClassName: testutil.TestGatewayClass,
	}

	s.reconciler = &controller.GRPCRouteReconciler{
		Client:         s.Client(),
		Log:            ctrl.Log.WithName("test").WithName("GRPCRouteDrift"),
		Scheme:         s.Client().Scheme(),
		PangolinClient: s.mockPangolin,
		Config:         cfg,
		Recorder:       record.NewFakeRecorder(100),
	}
}

func (s *GRPCRouteDriftTestSuite) TearDownTest() {
	ctx := s.Context()
	ns := client.InNamespace(s.testNamespace)

	var grpcRoutes gatewayv1.GRPCRouteList
	_ = s.Client().List(ctx, &grpcRoutes, ns)
	for i := range grpcRoutes.Items {
		gr := grpcRoutes.Items[i].DeepCopy()
		if len(gr.Finalizers) > 0 {
			patch := client.MergeFrom(gr.DeepCopy())
			gr.Finalizers = nil
			_ = s.Client().Patch(ctx, gr, patch)
		}
	}

	var gateways gatewayv1.GatewayList
	_ = s.Client().List(ctx, &gateways, ns)
	for i := range gateways.Items {
		gw := gateways.Items[i].DeepCopy()
		if len(gw.Finalizers) > 0 {
			patch := client.MergeFrom(gw.DeepCopy())
			gw.Finalizers = nil
			_ = s.Client().Patch(ctx, gw, patch)
		}
	}

	_ = s.Client().DeleteAllOf(ctx, &corev1.Service{}, ns)
	_ = s.Client().DeleteAllOf(ctx, &gatewayv1.GRPCRoute{}, ns)
	_ = s.Client().DeleteAllOf(ctx, &gatewayv1.Gateway{}, ns)

	s.Require().Eventually(func() bool {
		var gwList gatewayv1.GatewayList
		var grList gatewayv1.GRPCRouteList
		_ = s.Client().List(ctx, &gwList, ns)
		_ = s.Client().List(ctx, &grList, ns)
		return len(gwList.Items) == 0 && len(grList.Items) == 0
	}, 30*time.Second, 100*time.Millisecond, "gateways and grpcroutes should be deleted")

	s.mockPangolin.AssertExpectations(s.T())
}

// TestReconcileTargets_DeduplicatesBackends verifies that when two rules in a
// GRPCRoute reference the same service:port, only ONE target is created.
func (s *GRPCRouteDriftTestSuite) TestReconcileTargets_DeduplicatesBackends() {
	// Create Gateway with site ID
	gateway := testutil.NewTestGateway("grpc-dedup-gw", s.testNamespace)
	gateway.Labels = map[string]string{
		controller.SiteIDLabel: testutil.TestSiteID,
	}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// Create the backend Service
	svc := testutil.NewTestService("grpc-dedup-svc", s.testNamespace)
	err = s.Client().Create(s.Context(), svc)
	s.Require().NoError(err)
	// Re-fetch to get server-assigned ClusterIP
	err = s.Client().Get(s.Context(), client.ObjectKeyFromObject(svc), svc)
	s.Require().NoError(err)

	// Build a GRPCRoute with two rules referencing the same backend service:port.
	// The deduplication logic in reconcileTargets should only create one target.
	port9090 := gatewayv1.PortNumber(9090)
	svcName := gatewayv1.ObjectName("grpc-dedup-svc")
	grpcNS := gatewayv1.Namespace(s.testNamespace)
	kind := gatewayv1.Kind("Gateway")

	grpcRoute := &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "grpc-dedup-route",
			Namespace: s.testNamespace,
			Labels:    map[string]string{controller.ResourceIDLabel: "res-dedup"},
		},
		Spec: gatewayv1.GRPCRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Name:      "grpc-dedup-gw",
						Namespace: &grpcNS,
						Kind:      &kind,
					},
				},
			},
			Hostnames: []gatewayv1.Hostname{"grpc-dedup.example.com"},
			Rules: []gatewayv1.GRPCRouteRule{
				{
					BackendRefs: []gatewayv1.GRPCBackendRef{
						{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name: svcName,
									Port: &port9090,
								},
							},
						},
					},
				},
				// Second rule — same backend, must be deduplicated
				{
					BackendRefs: []gatewayv1.GRPCBackendRef{
						{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name: svcName,
									Port: &port9090,
								},
							},
						},
					},
				},
			},
		},
	}

	err = s.Client().Create(s.Context(), grpcRoute)
	s.Require().NoError(err)

	// Mock: verifyOrRecreateResource calls ListResources — resource exists
	s.mockPangolin.On("ListResources", testutil.MockAnything).Return([]map[string]interface{}{
		{"resourceId": "res-dedup", "name": "grpcroute-drift-ns-grpc-dedup-route"},
	}, nil).Once()

	// Mock: ListTargetsRaw returns empty (no existing targets yet)
	s.mockPangolin.On("ListTargetsRaw", testutil.MockAnything, "res-dedup").
		Return([]map[string]interface{}{}, nil).Once()

	// CreateTargetRaw must be called EXACTLY ONCE (deduplicated)
	createdTarget := map[string]interface{}{"targetId": float64(99)}
	s.mockPangolin.On("CreateTargetRaw", testutil.MockAnything, "res-dedup", testutil.MockAnything).
		Return(createdTarget, nil).Once()

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "grpc-dedup-route",
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().Greater(result.RequeueAfter, time.Duration(0),
		"should requeue for periodic verification")

	// AssertExpectations in TearDownTest confirms CreateTargetRaw was called only once
}

// TestHandleDelete_404Tolerance verifies that when DeleteResource returns a
// Pangolin 404 error (resource already gone), the finalizer is still removed
// and no error is returned.
func (s *GRPCRouteDriftTestSuite) TestHandleDelete_404Tolerance() {
	grpcNS := gatewayv1.Namespace(s.testNamespace)
	kind := gatewayv1.Kind("Gateway")

	grpcRoute := &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "grpc-404-tolerance",
			Namespace:  s.testNamespace,
			Labels:     map[string]string{controller.ResourceIDLabel: "res-gone"},
			Finalizers: []string{controller.FinalizerName},
		},
		Spec: gatewayv1.GRPCRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Name:      "grpc-gw-404",
						Namespace: &grpcNS,
						Kind:      &kind,
					},
				},
			},
		},
	}

	err := s.Client().Create(s.Context(), grpcRoute)
	s.Require().NoError(err)

	// Mark for deletion
	err = s.Client().Delete(s.Context(), grpcRoute)
	s.Require().NoError(err)

	// Mock: DeleteResource returns 404 — resource already removed from Pangolin
	s.mockPangolin.On("DeleteResource", testutil.MockAnything, "res-gone").
		Return(&pangolin.PangolinAPIError{
			StatusCode: 404,
			Method:     "DELETE",
			Endpoint:   "/resource/res-gone",
			Message:    "not found",
		}).Once()

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "grpc-404-tolerance",
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)

	// 404 during deletion must NOT be treated as an error
	s.Require().NoError(err, "404 from DeleteResource should be tolerated")
	s.Require().False(result.Requeue)

	// Finalizer must be removed (k8s will then fully delete the object)
	s.Require().Eventually(func() bool {
		fresh := &gatewayv1.GRPCRoute{}
		if getErr := s.Client().Get(s.Context(), client.ObjectKeyFromObject(grpcRoute), fresh); getErr != nil {
			return true // NotFound — fully deleted, finalizer gone
		}
		for _, f := range fresh.Finalizers {
			if f == controller.FinalizerName {
				return false // finalizer still present
			}
		}
		return true
	}, 10*time.Second, 100*time.Millisecond,
		"finalizer should be removed after 404-tolerant deletion")
}

func TestGRPCRouteDriftSuite(t *testing.T) {
	suite.Run(t, new(GRPCRouteDriftTestSuite))
}
