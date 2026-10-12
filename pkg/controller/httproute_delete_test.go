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

// TestHandleDelete_ListResourcesFailsContinues verifies that when ListResources
// fails during deletion the controller does NOT block deletion — it proceeds with
// an empty resource map (logging the error), removes the finalizer, and returns
// success. We must never leave resources stuck in Terminating due to a list error.
func (s *HTTPRouteControllerTestSuite) TestHandleDelete_ListResourcesFailsContinues() {
	gateway := testutil.NewTestGateway("gw-lr-fail", s.testNamespace)
	gateway.Labels = map[string]string{controller.SiteIDLabel: testutil.TestSiteID}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	route := testutil.NewTestHTTPRoute("delete-lr-fail", s.testNamespace, "gw-lr-fail", "app.example.com")
	route.Finalizers = []string{controller.FinalizerName}
	err = s.Client().Create(s.Context(), route)
	s.Require().NoError(err)

	err = s.Client().Delete(s.Context(), route)
	s.Require().NoError(err)

	// ListResources fails — controller must continue, not block deletion
	s.mockPangolin.On("ListResources", testutil.MockAnything).
		Return(nil, fmt.Errorf("pangolin API down")).Once()

	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name: "delete-lr-fail", Namespace: s.testNamespace,
	}}
	result, err := s.reconciler.Reconcile(s.Context(), req)

	// Resilient deletion: error is logged but NOT returned; finalizer IS removed.
	s.Require().NoError(err, "ListResources failure must not block deletion")
	s.Require().Zero(result.RequeueAfter)

	s.Eventually(func() bool {
		fresh := &gatewayv1.HTTPRoute{}
		return s.Client().Get(s.Context(),
			types.NamespacedName{Name: "delete-lr-fail", Namespace: s.testNamespace},
			fresh) != nil // NotFound = finalizer removed and object gone
	}, "finalizer must be removed even when ListResources fails")
}

// TestHandleDelete_HostnameNotInResourcesSkips verifies that when a hostname on
// the route has no matching resource in Pangolin (already cleaned up or never
// created) the deletion loop skips it cleanly and the finalizer is removed.
func (s *HTTPRouteControllerTestSuite) TestHandleDelete_HostnameNotInResourcesSkips() {
	gateway := testutil.NewTestGateway("gw-no-res", s.testNamespace)
	gateway.Labels = map[string]string{controller.SiteIDLabel: testutil.TestSiteID}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	route := testutil.NewTestHTTPRoute("delete-no-res", s.testNamespace, "gw-no-res", "orphan.example.com")
	route.Finalizers = []string{controller.FinalizerName}
	err = s.Client().Create(s.Context(), route)
	s.Require().NoError(err)

	err = s.Client().Delete(s.Context(), route)
	s.Require().NoError(err)

	// ListResources returns resources, but none match "orphan.example.com"
	s.mockPangolin.On("ListResources", testutil.MockAnything).Return([]map[string]interface{}{
		{"name": "other.example.com", "resourceId": "res-other"},
	}, nil).Once()

	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name: "delete-no-res", Namespace: s.testNamespace,
	}}
	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().Zero(result.RequeueAfter)

	// No Pangolin target/resource delete calls should be made
	s.mockPangolin.AssertNotCalled(s.T(), "ListTargetsRaw")
	s.mockPangolin.AssertNotCalled(s.T(), "DeleteTarget")

	s.Eventually(func() bool {
		fresh := &gatewayv1.HTTPRoute{}
		return s.Client().Get(s.Context(),
			types.NamespacedName{Name: "delete-no-res", Namespace: s.testNamespace},
			fresh) != nil
	}, "finalizer must be removed when no resources exist for the route's hostnames")
}

// TestHandleDelete_ListTargetsRawFailsContinues verifies that when listing
// targets for one hostname fails, the controller logs the error, skips that
// hostname, and still processes the remaining hostnames (and ultimately removes
// the finalizer if no other errors occurred).
func (s *HTTPRouteControllerTestSuite) TestHandleDelete_ListTargetsRawFailsContinues() {
	gateway := testutil.NewTestGateway("gw-lt-fail", s.testNamespace)
	gateway.Labels = map[string]string{controller.SiteIDLabel: testutil.TestSiteID}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	route := testutil.NewTestHTTPRoute("delete-lt-fail", s.testNamespace, "gw-lt-fail", "app.example.com")
	route.Finalizers = []string{controller.FinalizerName}
	err = s.Client().Create(s.Context(), route)
	s.Require().NoError(err)

	err = s.Client().Delete(s.Context(), route)
	s.Require().NoError(err)

	s.mockPangolin.On("ListResources", testutil.MockAnything).Return([]map[string]interface{}{
		{"name": "app.example.com", "resourceId": "res-lt-fail"},
	}, nil).Once()

	// ListTargetsRaw fails for this resource
	s.mockPangolin.On("ListTargetsRaw", testutil.MockAnything, "res-lt-fail").
		Return(nil, fmt.Errorf("timeout listing targets")).Once()

	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name: "delete-lt-fail", Namespace: s.testNamespace,
	}}
	result, err := s.reconciler.Reconcile(s.Context(), req)
	// The controller skips this hostname and continues — no error returned
	s.Require().NoError(err)
	s.Require().Zero(result.RequeueAfter)

	s.Eventually(func() bool {
		fresh := &gatewayv1.HTTPRoute{}
		return s.Client().Get(s.Context(),
			types.NamespacedName{Name: "delete-lt-fail", Namespace: s.testNamespace},
			fresh) != nil
	}, "finalizer must be removed even when ListTargetsRaw fails")
}

// TestHandleDelete_LegacyTargetNoLabels verifies that targets without ownership
// labels (created before label tracking was introduced) are deleted for safety
// when the owning HTTPRoute is being deleted.
func (s *HTTPRouteControllerTestSuite) TestHandleDelete_LegacyTargetNoLabels() {
	gateway := testutil.NewTestGateway("gw-legacy", s.testNamespace)
	gateway.Labels = map[string]string{controller.SiteIDLabel: testutil.TestSiteID}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	route := testutil.NewTestHTTPRoute("delete-legacy", s.testNamespace, "gw-legacy", "legacy.example.com")
	route.Finalizers = []string{controller.FinalizerName}
	err = s.Client().Create(s.Context(), route)
	s.Require().NoError(err)

	err = s.Client().Delete(s.Context(), route)
	s.Require().NoError(err)

	s.mockPangolin.On("ListResources", testutil.MockAnything).Return([]map[string]interface{}{
		{"name": "legacy.example.com", "resourceId": "res-legacy"},
	}, nil).Once()

	// Legacy target — has no ownership labels
	s.mockPangolin.On("ListTargetsRaw", testutil.MockAnything, "res-legacy").Return([]map[string]interface{}{
		{
			"targetId": float64(99),
			// No "labels" key — this is a legacy target
		},
	}, nil).Once()
	s.mockPangolin.On("DeleteTarget", testutil.MockAnything, "99").Return(nil).Once()
	// All targets removed → delete the resource too
	s.mockPangolin.On("DeleteResource", testutil.MockAnything, "res-legacy").Return(nil).Once()

	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name: "delete-legacy", Namespace: s.testNamespace,
	}}
	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().Zero(result.RequeueAfter)

	s.Eventually(func() bool {
		fresh := &gatewayv1.HTTPRoute{}
		return s.Client().Get(s.Context(),
			types.NamespacedName{Name: "delete-legacy", Namespace: s.testNamespace},
			fresh) != nil
	}, "finalizer must be removed after legacy target deletion")
}

// TestHandleDelete_TargetFromOtherRouteSkipped verifies that targets owned by a
// different HTTPRoute are preserved during deletion. Only this route's targets
// should be deleted; shared resources must remain active for the other route.
func (s *HTTPRouteControllerTestSuite) TestHandleDelete_TargetFromOtherRouteSkipped() {
	gateway := testutil.NewTestGateway("gw-other", s.testNamespace)
	gateway.Labels = map[string]string{controller.SiteIDLabel: testutil.TestSiteID}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	route := testutil.NewTestHTTPRoute("delete-shared-res", s.testNamespace, "gw-other", "shared.example.com")
	route.Finalizers = []string{controller.FinalizerName}
	err = s.Client().Create(s.Context(), route)
	s.Require().NoError(err)

	err = s.Client().Delete(s.Context(), route)
	s.Require().NoError(err)

	s.mockPangolin.On("ListResources", testutil.MockAnything).Return([]map[string]interface{}{
		{"name": "shared.example.com", "resourceId": "res-shared"},
	}, nil).Once()

	// Two targets: one owned by this route, one owned by a different route
	s.mockPangolin.On("ListTargetsRaw", testutil.MockAnything, "res-shared").Return([]map[string]interface{}{
		{
			"targetId": float64(10),
			"labels": map[string]interface{}{
				"gateway.pangolin.net/httproute-name":      "delete-shared-res",
				"gateway.pangolin.net/httproute-namespace": s.testNamespace,
			},
		},
		{
			"targetId": float64(20),
			"labels": map[string]interface{}{
				"gateway.pangolin.net/httproute-name":      "other-route", // different route!
				"gateway.pangolin.net/httproute-namespace": s.testNamespace,
			},
		},
	}, nil).Once()

	// Only our target (10) must be deleted; target 20 must be preserved
	s.mockPangolin.On("DeleteTarget", testutil.MockAnything, "10").Return(nil).Once()
	// Resource has remaining target (20) so must NOT be deleted
	// (no DeleteResource call expected)

	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name: "delete-shared-res", Namespace: s.testNamespace,
	}}
	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().Zero(result.RequeueAfter)

	// DeleteResource must NOT be called — the other route's target still lives there
	s.mockPangolin.AssertNotCalled(s.T(), "DeleteResource")

	s.Eventually(func() bool {
		fresh := &gatewayv1.HTTPRoute{}
		return s.Client().Get(s.Context(),
			types.NamespacedName{Name: "delete-shared-res", Namespace: s.testNamespace},
			fresh) != nil
	}, "finalizer must be removed after partial target deletion")
}

// TestHandleDelete_DeleteResourceFailsQueuesRetry verifies that when all targets
// of a route are deleted but DeleteResource fails for the Pangolin resource, the
// error is collected and the route requeues rather than silently succeeding.
func (s *HTTPRouteControllerTestSuite) TestHandleDelete_DeleteResourceFailsQueuesRetry() {
	gateway := testutil.NewTestGateway("gw-delres", s.testNamespace)
	gateway.Labels = map[string]string{controller.SiteIDLabel: testutil.TestSiteID}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	route := testutil.NewTestHTTPRoute("delete-res-fail", s.testNamespace, "gw-delres", "res-fail.example.com")
	route.Finalizers = []string{controller.FinalizerName}
	err = s.Client().Create(s.Context(), route)
	s.Require().NoError(err)

	err = s.Client().Delete(s.Context(), route)
	s.Require().NoError(err)

	s.mockPangolin.On("ListResources", testutil.MockAnything).Return([]map[string]interface{}{
		{"name": "res-fail.example.com", "resourceId": "res-del-fail"},
	}, nil).Once()

	// One target owned by this route — deletion succeeds
	s.mockPangolin.On("ListTargetsRaw", testutil.MockAnything, "res-del-fail").Return([]map[string]interface{}{
		{
			"targetId": float64(5),
			"labels": map[string]interface{}{
				"gateway.pangolin.net/httproute-name":      "delete-res-fail",
				"gateway.pangolin.net/httproute-namespace": s.testNamespace,
			},
		},
	}, nil).Once()
	s.mockPangolin.On("DeleteTarget", testutil.MockAnything, "5").Return(nil).Once()

	// All targets deleted; now DeleteResource fails
	s.mockPangolin.On("DeleteResource", testutil.MockAnything, "res-del-fail").
		Return(fmt.Errorf("resource locked")).Once()

	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name: "delete-res-fail", Namespace: s.testNamespace,
	}}
	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().Error(err, "DeleteResource failure must propagate as error")
	s.Require().Greater(result.RequeueAfter, time.Duration(0))

	// Finalizer must remain so deletion is retried
	s.Eventually(func() bool {
		fresh := &gatewayv1.HTTPRoute{}
		if getErr := s.Client().Get(s.Context(),
			types.NamespacedName{Name: "delete-res-fail", Namespace: s.testNamespace},
			fresh); getErr != nil {
			return false
		}
		for _, f := range fresh.Finalizers {
			if f == controller.FinalizerName {
				return true
			}
		}
		return false
	}, 10*time.Second, 100*time.Millisecond, "finalizer must remain when DeleteResource fails")

	finalRoute := &gatewayv1.HTTPRoute{}
	if getErr := s.Client().Get(s.Context(),
		types.NamespacedName{Name: "delete-res-fail", Namespace: s.testNamespace},
		finalRoute); getErr == nil {
		patch := client.MergeFrom(finalRoute.DeepCopy())
		finalRoute.Finalizers = nil
		_ = s.Client().Patch(s.Context(), finalRoute, patch)
	}
}
