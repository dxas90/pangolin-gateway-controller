package controller_test

import (
	"fmt"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/controller"
	"github.com/dxas90/pangolin-gateway-controller/pkg/testutil"
	"github.com/stretchr/testify/mock"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// TestHandleDelete_PartialTargetDeletionFailure verifies that when one target
// deletion fails the finalizer is NOT removed and the reconciler returns an
// error with a non-zero RequeueAfter so the deletion will be retried.
func (s *HTTPRouteControllerTestSuite) TestHandleDelete_PartialTargetDeletionFailure() {
	// Create a Gateway so the route can be looked up
	gateway := testutil.NewTestGateway(testutil.TestGatewayName, s.testNamespace)
	gateway.Labels = map[string]string{
		controller.SiteIDLabel: testutil.TestSiteID,
	}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// Create an HTTPRoute with the finalizer already in place
	route := testutil.NewTestHTTPRoute("delete-partial", s.testNamespace, testutil.TestGatewayName, "app.example.com")
	route.Finalizers = []string{controller.FinalizerName}
	err = s.Client().Create(s.Context(), route)
	s.Require().NoError(err)

	// Mark the route for deletion
	err = s.Client().Delete(s.Context(), route)
	s.Require().NoError(err)

	// Mock: ListResources returns the resource for this hostname
	s.mockPangolin.On("ListResources", testutil.MockAnything).Return([]map[string]interface{}{
		{"name": "app.example.com", "resourceId": "res-partial"},
	}, nil).Once()

	// Mock: ListTargetsRaw returns two targets both owned by this route
	s.mockPangolin.On("ListTargetsRaw", testutil.MockAnything, "res-partial").Return([]map[string]interface{}{
		{
			"targetId": float64(1),
			"labels": map[string]interface{}{
				"gateway.pangolin.net/httproute-name":      "delete-partial",
				"gateway.pangolin.net/httproute-namespace": s.testNamespace,
			},
		},
		{
			"targetId": float64(2),
			"labels": map[string]interface{}{
				"gateway.pangolin.net/httproute-name":      "delete-partial",
				"gateway.pangolin.net/httproute-namespace": s.testNamespace,
			},
		},
	}, nil).Once()

	// First target deletion fails; second succeeds
	s.mockPangolin.On("DeleteTarget", testutil.MockAnything, "1").Return(fmt.Errorf("network error")).Once()
	s.mockPangolin.On("DeleteTarget", testutil.MockAnything, "2").Return(nil).Once()

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "delete-partial",
			Namespace: s.testNamespace,
		},
	}
	result, err := s.reconciler.Reconcile(s.Context(), req)

	// Reconciler must return an error so the work-queue knows to retry
	s.Require().Error(err, "expected error when target deletion partially failed")

	// RequeueAfter must be non-zero so the item is retried after a delay
	s.Require().Greater(result.RequeueAfter, time.Duration(0),
		"expected non-zero RequeueAfter when deletion partially failed")

	// The finalizer must NOT have been removed — the route must still exist
	// with the finalizer intact
	s.Require().Eventually(func() bool {
		fresh := &gatewayv1.HTTPRoute{}
		if getErr := s.Client().Get(
			s.Context(),
			types.NamespacedName{Name: "delete-partial", Namespace: s.testNamespace},
			fresh,
		); getErr != nil {
			return false
		}
		for _, f := range fresh.Finalizers {
			if f == controller.FinalizerName {
				return true
			}
		}
		return false
	}, 10*time.Second, 100*time.Millisecond,
		"finalizer should remain when deletion partially failed")

	// Prevent TearDownTest from complaining about unexpected mock calls by
	// clearing the route's finalizer so normal deletion can proceed
	finalRoute := &gatewayv1.HTTPRoute{}
	if getErr := s.Client().Get(
		s.Context(),
		types.NamespacedName{Name: "delete-partial", Namespace: s.testNamespace},
		finalRoute,
	); getErr == nil {
		patch := client.MergeFrom(finalRoute.DeepCopy())
		finalRoute.Finalizers = nil
		_ = s.Client().Patch(s.Context(), finalRoute, patch)
	}
}

// TestHandleDelete_AllTargetsFail verifies that a complete failure to delete
// any targets still returns an error and keeps the finalizer.
func (s *HTTPRouteControllerTestSuite) TestHandleDelete_AllTargetsFail() {
	gateway := testutil.NewTestGateway(testutil.TestGatewayName, s.testNamespace)
	gateway.Labels = map[string]string{
		controller.SiteIDLabel: testutil.TestSiteID,
	}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	route := testutil.NewTestHTTPRoute("delete-all-fail", s.testNamespace, testutil.TestGatewayName, "all-fail.example.com")
	route.Finalizers = []string{controller.FinalizerName}
	err = s.Client().Create(s.Context(), route)
	s.Require().NoError(err)

	err = s.Client().Delete(s.Context(), route)
	s.Require().NoError(err)

	s.mockPangolin.On("ListResources", testutil.MockAnything).Return([]map[string]interface{}{
		{"name": "all-fail.example.com", "resourceId": "res-all-fail"},
	}, nil).Once()

	s.mockPangolin.On("ListTargetsRaw", testutil.MockAnything, "res-all-fail").Return([]map[string]interface{}{
		{
			"targetId": float64(10),
			"labels": map[string]interface{}{
				"gateway.pangolin.net/httproute-name":      "delete-all-fail",
				"gateway.pangolin.net/httproute-namespace": s.testNamespace,
			},
		},
	}, nil).Once()

	s.mockPangolin.On("DeleteTarget", testutil.MockAnything, mock.MatchedBy(func(id string) bool { return id == "10" })).
		Return(fmt.Errorf("timeout")).Once()

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "delete-all-fail",
			Namespace: s.testNamespace,
		},
	}
	result, err := s.reconciler.Reconcile(s.Context(), req)

	s.Require().Error(err)
	s.Require().Greater(result.RequeueAfter, time.Duration(0))

	// Strip finalizer so TearDownTest can clean up
	finalRoute := &gatewayv1.HTTPRoute{}
	if getErr := s.Client().Get(
		s.Context(),
		types.NamespacedName{Name: "delete-all-fail", Namespace: s.testNamespace},
		finalRoute,
	); getErr == nil {
		patch := client.MergeFrom(finalRoute.DeepCopy())
		finalRoute.Finalizers = nil
		_ = s.Client().Patch(s.Context(), finalRoute, patch)
	}
}
