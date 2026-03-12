package controller_test

import (
	"testing"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/config"
	"github.com/dxas90/pangolin-gateway-controller/pkg/controller"
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

// GRPCRouteControllerTestSuite tests the GRPCRouteReconciler.
type GRPCRouteControllerTestSuite struct {
	testutil.EnvTestSuite
	reconciler    *controller.GRPCRouteReconciler
	mockPangolin  *testutil.MockPangolinClient
	testNamespace string
}

func (s *GRPCRouteControllerTestSuite) SetupSuite() {
	s.EnvTestSuite.SetupSuite()
	s.testNamespace = "grpcroute-test-ns"

	ns := testutil.NewTestNamespace(s.testNamespace)
	err := s.Client().Create(s.Context(), ns)
	s.Require().NoError(err)
}

func (s *GRPCRouteControllerTestSuite) SetupTest() {
	s.mockPangolin = testutil.NewMockPangolinClient()

	cfg := &config.ControllerConfig{
		GatewayClassName: testutil.TestGatewayClass,
	}

	s.reconciler = &controller.GRPCRouteReconciler{
		Client:         s.Client(),
		Log:            ctrl.Log.WithName("test").WithName("GRPCRoute"),
		Scheme:         s.Client().Scheme(),
		PangolinClient: s.mockPangolin,
		Config:         cfg,
		Recorder:       record.NewFakeRecorder(100),
	}
}

func (s *GRPCRouteControllerTestSuite) TearDownTest() {
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

// TestReconcile_DeleteGRPCRoute tests GRPCRoute deletion with finalizer and resource cleanup.
func (s *GRPCRouteControllerTestSuite) TestReconcile_DeleteGRPCRoute() {
	// Create Gateway
	gateway := testutil.NewTestGateway("grpc-gw", s.testNamespace)
	gateway.Labels = map[string]string{
		controller.SiteIDLabel: testutil.TestSiteID,
	}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// Create GRPCRoute with finalizer and resource ID
	grpcRoute := testutil.NewTestGRPCRoute("grpc-delete-test", s.testNamespace, "grpc-gw", testutil.TestHostname)
	grpcRoute.Finalizers = []string{controller.FinalizerName}
	grpcRoute.Labels = map[string]string{
		controller.ResourceIDLabel: "resource-789",
	}
	err = s.Client().Create(s.Context(), grpcRoute)
	s.Require().NoError(err)

	// Mock expects delete call
	s.mockPangolin.On("DeleteResource", testutil.MockAnything, "resource-789").Return(nil).Once()

	// Delete GRPCRoute
	err = s.Client().Delete(s.Context(), grpcRoute)
	s.Require().NoError(err)

	// Reconcile to process deletion
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "grpc-delete-test",
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)

	// Verify GRPCRoute was deleted (finalizer removed)
	s.Eventually(func() bool {
		fresh := &gatewayv1.GRPCRoute{}
		err := s.Client().Get(s.Context(), client.ObjectKeyFromObject(grpcRoute), fresh)
		return err != nil // GRPCRoute should not exist
	}, "GRPCRoute should be deleted")
}

// TestReconcile_DeleteGRPCRoute_NoResourceID tests deletion when no resource ID is labeled.
func (s *GRPCRouteControllerTestSuite) TestReconcile_DeleteGRPCRoute_NoResourceID() {
	// Create GRPCRoute with finalizer but NO resource ID
	grpcRoute := testutil.NewTestGRPCRoute("grpc-noid-test", s.testNamespace, "grpc-gw", testutil.TestHostname)
	grpcRoute.Finalizers = []string{controller.FinalizerName}
	err := s.Client().Create(s.Context(), grpcRoute)
	s.Require().NoError(err)

	// Delete GRPCRoute
	err = s.Client().Delete(s.Context(), grpcRoute)
	s.Require().NoError(err)

	// Reconcile - should skip Pangolin cleanup
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "grpc-noid-test",
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)

	// GRPCRoute should be deleted - no Pangolin calls made
	s.Eventually(func() bool {
		fresh := &gatewayv1.GRPCRoute{}
		err := s.Client().Get(s.Context(), client.ObjectKeyFromObject(grpcRoute), fresh)
		return err != nil
	}, "GRPCRoute should be deleted without resource cleanup")
}

// TestReconcile_NotFound tests reconciling a non-existent GRPCRoute.
func (s *GRPCRouteControllerTestSuite) TestReconcile_NotFound() {
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "nonexistent-grpcroute",
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)
}

// TestReconcile_NoParentGateway tests GRPCRoute without parent gateway references.
func (s *GRPCRouteControllerTestSuite) TestReconcile_NoParentGateway() {
	grpcRoute := &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "grpc-no-parent",
			Namespace: s.testNamespace,
		},
		Spec: gatewayv1.GRPCRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{}, // empty
			},
		},
	}

	err := s.Client().Create(s.Context(), grpcRoute)
	s.Require().NoError(err)

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "grpc-no-parent",
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)
}

// TestReconcile_GatewayNotReady tests GRPCRoute when Gateway doesn't have site ID.
func (s *GRPCRouteControllerTestSuite) TestReconcile_GatewayNotReady() {
	// Create Gateway without site ID
	gateway := testutil.NewTestGateway("grpc-gw-notready", s.testNamespace)
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	grpcRoute := testutil.NewTestGRPCRoute("grpc-notready", s.testNamespace, "grpc-gw-notready", testutil.TestHostname)
	err = s.Client().Create(s.Context(), grpcRoute)
	s.Require().NoError(err)

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "grpc-notready",
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().Equal(10*time.Second, result.RequeueAfter)
}

func TestGRPCRouteControllerSuite(t *testing.T) {
	suite.Run(t, new(GRPCRouteControllerTestSuite))
}
