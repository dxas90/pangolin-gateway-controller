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

// HTTPRouteControllerTestSuite tests the HTTPRouteReconciler.
type HTTPRouteControllerTestSuite struct {
	testutil.EnvTestSuite
	reconciler    *controller.HTTPRouteReconciler
	mockPangolin  *testutil.MockPangolinClient
	testNamespace string
}

// SetupSuite runs once before all tests.
func (s *HTTPRouteControllerTestSuite) SetupSuite() {
	s.EnvTestSuite.SetupSuite()
	s.testNamespace = testutil.TestNamespace
}

// SetupTest runs before each test.
func (s *HTTPRouteControllerTestSuite) SetupTest() {
	// Create test namespace
	ns := testutil.NewTestNamespace(s.testNamespace)
	err := s.Client().Create(s.Context(), ns)
	s.Require().NoError(err)

	// Create fresh mock for each test
	s.mockPangolin = testutil.NewMockPangolinClient()

	// Create controller config
	cfg := &config.ControllerConfig{
		GatewayClassName: testutil.TestGatewayClass,
	}

	// Create reconciler with mock client
	s.reconciler = &controller.HTTPRouteReconciler{
		Client:         s.Client(),
		Log:            ctrl.Log.WithName("test").WithName("HTTPRoute"),
		Scheme:         s.Client().Scheme(),
		PangolinClient: s.mockPangolin,
		Config:         cfg,
		Recorder:       record.NewFakeRecorder(100),
	}
}

// TearDownTest runs after each test.
func (s *HTTPRouteControllerTestSuite) TearDownTest() {
	// Clean up namespace
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: s.testNamespace,
		},
	}
	_ = s.Client().Delete(s.Context(), ns)

	// Verify all mock expectations
	s.mockPangolin.AssertExpectations(s.T())
}

// TestReconcile_NewHTTPRoute tests reconciling a new HTTPRoute.
func (s *HTTPRouteControllerTestSuite) TestReconcile_NewHTTPRoute() {
	// Create Gateway first
	gateway := testutil.NewTestGateway(testutil.TestGatewayName, s.testNamespace)
	gateway.Labels = map[string]string{
		controller.SiteIDLabel: testutil.TestSiteID,
	}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// Create backend Service
	service := testutil.NewTestService(testutil.TestServiceName, s.testNamespace)
	err = s.Client().Create(s.Context(), service)
	s.Require().NoError(err)

	// Create HTTPRoute
	httpRoute := testutil.NewTestHTTPRoute(testutil.TestHTTPRouteName, s.testNamespace, testutil.TestGatewayName, testutil.TestHostname)
	err = s.Client().Create(s.Context(), httpRoute)
	s.Require().NoError(err)

	// Setup mock expectations
	domains := []map[string]interface{}{
		{
			"baseDomain": "example.com",
			"domainId":   "domain1",
		},
	}

	resource := map[string]interface{}{
		"resourceId": "resource-123",
		"name":       testutil.TestHostname,
		"subdomain":  "test",
	}

	target := map[string]interface{}{
		"targetId": float64(456),
		"ip":       service.Spec.ClusterIP,
		"port":     float64(80),
		"path":     "/",
	}

	s.mockPangolin.On("ListDomains", s.Context()).Return(domains, nil).Maybe()
	s.mockPangolin.On("ListResources", s.Context()).Return([]map[string]interface{}{}, nil).Once()
	s.mockPangolin.On("CreateResource", s.Context(), testutil.MockAnything).Return(resource, nil).Once()
	s.mockPangolin.On("UpdateResource", s.Context(), "resource-123", testutil.MockAnything).Return(nil).Maybe()
	s.mockPangolin.On("DisableSSO", s.Context(), "resource-123").Return(nil).Maybe()
	s.mockPangolin.On("ListTargetsRaw", s.Context(), "resource-123").Return([]map[string]interface{}{}, nil).Once()
	s.mockPangolin.On("CreateTargetRaw", s.Context(), "resource-123", testutil.MockAnything).Return(target, nil).Once()

	// Reconcile
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testutil.TestHTTPRouteName,
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().Equal(5*time.Minute, result.RequeueAfter, "Should requeue after 5 minutes for periodic verification")

	// Verify HTTPRoute status was updated
	s.Eventually(func() bool {
		fresh := &gatewayv1.HTTPRoute{}
		err := s.Client().Get(s.Context(), client.ObjectKeyFromObject(httpRoute), fresh)
		if err != nil {
			return false
		}

		// Check for Accepted condition
		for _, status := range fresh.Status.RouteStatus.Parents {
			for _, cond := range status.Conditions {
				if cond.Type == string(gatewayv1.RouteConditionAccepted) {
					return cond.Status == metav1.ConditionTrue
				}
			}
		}
		return false
	}, "HTTPRoute should be accepted")
}

// TestReconcile_DeleteHTTPRoute tests HTTPRoute deletion with finalizer.
func (s *HTTPRouteControllerTestSuite) TestReconcile_DeleteHTTPRoute() {
	// Create Gateway
	gateway := testutil.NewTestGateway(testutil.TestGatewayName, s.testNamespace)
	gateway.Labels = map[string]string{
		controller.SiteIDLabel: testutil.TestSiteID,
	}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// Create HTTPRoute with finalizer
	httpRoute := testutil.NewTestHTTPRoute(testutil.TestHTTPRouteName, s.testNamespace, testutil.TestGatewayName, testutil.TestHostname)
	httpRoute.Finalizers = []string{controller.FinalizerName}
	err = s.Client().Create(s.Context(), httpRoute)
	s.Require().NoError(err)

	// Setup mock expectations for deletion
	resources := []map[string]interface{}{
		{
			"resourceId": "resource-123",
			"name":       testutil.TestHostname,
		},
	}

	targets := []map[string]interface{}{
		{
			"targetId": float64(456),
			"ip":       "10.96.0.100",
			"port":     float64(80),
			"path":     "/",
			"labels": map[string]interface{}{
				"gateway.pangolin.net/httproute-name":      testutil.TestHTTPRouteName,
				"gateway.pangolin.net/httproute-namespace": s.testNamespace,
			},
		},
	}

	s.mockPangolin.On("ListResources", s.Context()).Return(resources, nil).Once()
	s.mockPangolin.On("ListTargetsRaw", s.Context(), "resource-123").Return(targets, nil).Twice()
	s.mockPangolin.On("DeleteTarget", s.Context(), "456").Return(nil).Once()
	s.mockPangolin.On("DeleteResource", s.Context(), "resource-123").Return(nil).Maybe()

	// Delete HTTPRoute
	err = s.Client().Delete(s.Context(), httpRoute)
	s.Require().NoError(err)

	// Reconcile to process deletion
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testutil.TestHTTPRouteName,
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)

	// Verify HTTPRoute was deleted (finalizer removed)
	s.Eventually(func() bool {
		fresh := &gatewayv1.HTTPRoute{}
		err := s.Client().Get(s.Context(), client.ObjectKeyFromObject(httpRoute), fresh)
		return err != nil // HTTPRoute should not exist
	}, "HTTPRoute should be deleted")
}

// TestReconcile_NoParentGateway tests error handling when parent gateway is missing.
func (s *HTTPRouteControllerTestSuite) TestReconcile_NoParentGateway() {
	// Create HTTPRoute without Gateway
	httpRoute := testutil.NewTestHTTPRoute(testutil.TestHTTPRouteName, s.testNamespace, testutil.TestGatewayName, testutil.TestHostname)
	err := s.Client().Create(s.Context(), httpRoute)
	s.Require().NoError(err)

	// Reconcile
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testutil.TestHTTPRouteName,
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().Error(err, "Should error when parent gateway not found")
	s.Require().Equal(30*time.Second, result.RequeueAfter, "Should requeue after 30s on error")

	// Verify status reflects the error
	s.Eventually(func() bool {
		fresh := &gatewayv1.HTTPRoute{}
		err := s.Client().Get(s.Context(), client.ObjectKeyFromObject(httpRoute), fresh)
		if err != nil {
			return false
		}

		// Check for error condition
		for _, status := range fresh.Status.RouteStatus.Parents {
			for _, cond := range status.Conditions {
				if cond.Type == string(gatewayv1.RouteConditionAccepted) {
					return cond.Status == metav1.ConditionFalse &&
						cond.Reason == "ParentGatewayNotFound"
				}
			}
		}
		return false
	}, "HTTPRoute should have ParentGatewayNotFound status")
}

// TestReconcile_GatewayNotReady tests when Gateway doesn't have site ID yet.
func (s *HTTPRouteControllerTestSuite) TestReconcile_GatewayNotReady() {
	// Create Gateway without site ID
	gateway := testutil.NewTestGateway(testutil.TestGatewayName, s.testNamespace)
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// Create HTTPRoute
	httpRoute := testutil.NewTestHTTPRoute(testutil.TestHTTPRouteName, s.testNamespace, testutil.TestGatewayName, testutil.TestHostname)
	err = s.Client().Create(s.Context(), httpRoute)
	s.Require().NoError(err)

	// Reconcile
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testutil.TestHTTPRouteName,
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err, "Should not error, but wait for Gateway to be ready")
	s.Require().Equal(10*time.Second, result.RequeueAfter, "Should requeue after 10s")

	// Verify status reflects Gateway not ready
	s.Eventually(func() bool {
		fresh := &gatewayv1.HTTPRoute{}
		err := s.Client().Get(s.Context(), client.ObjectKeyFromObject(httpRoute), fresh)
		if err != nil {
			return false
		}

		for _, status := range fresh.Status.RouteStatus.Parents {
			for _, cond := range status.Conditions {
				if cond.Type == string(gatewayv1.RouteConditionAccepted) {
					return cond.Status == metav1.ConditionFalse &&
						cond.Reason == "GatewayNotReady"
				}
			}
		}
		return false
	}, "HTTPRoute should have GatewayNotReady status")
}

// TestReconcile_SharedResource tests multiple HTTPRoutes sharing the same hostname/resource.
func (s *HTTPRouteControllerTestSuite) TestReconcile_SharedResource() {
	// Create Gateway
	gateway := testutil.NewTestGateway(testutil.TestGatewayName, s.testNamespace)
	gateway.Labels = map[string]string{
		controller.SiteIDLabel: testutil.TestSiteID,
	}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// Create backend Service
	service := testutil.NewTestService(testutil.TestServiceName, s.testNamespace)
	err = s.Client().Create(s.Context(), service)
	s.Require().NoError(err)

	// Create first HTTPRoute
	httpRoute1 := testutil.NewTestHTTPRoute("route-1", s.testNamespace, testutil.TestGatewayName, testutil.TestHostname)
	err = s.Client().Create(s.Context(), httpRoute1)
	s.Require().NoError(err)

	// Setup mock expectations for first route
	domains := []map[string]interface{}{
		{
			"baseDomain": "example.com",
			"domainId":   "domain1",
		},
	}

	resource := map[string]interface{}{
		"resourceId": "resource-123",
		"name":       testutil.TestHostname,
	}

	target1 := map[string]interface{}{
		"targetId": float64(456),
		"ip":       service.Spec.ClusterIP,
		"port":     float64(80),
		"path":     "/",
	}

	s.mockPangolin.On("ListDomains", s.Context()).Return(domains, nil).Maybe()
	s.mockPangolin.On("ListResources", s.Context()).Return([]map[string]interface{}{}, nil).Once()
	s.mockPangolin.On("CreateResource", s.Context(), testutil.MockAnything).Return(resource, nil).Once()
	s.mockPangolin.On("UpdateResource", s.Context(), "resource-123", testutil.MockAnything).Return(nil).Maybe()
	s.mockPangolin.On("DisableSSO", s.Context(), "resource-123").Return(nil).Maybe()
	s.mockPangolin.On("ListTargetsRaw", s.Context(), "resource-123").Return([]map[string]interface{}{}, nil).Once()
	s.mockPangolin.On("CreateTargetRaw", s.Context(), "resource-123", testutil.MockAnything).Return(target1, nil).Once()

	// Reconcile first route
	req1 := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "route-1",
			Namespace: s.testNamespace,
		},
	}

	_, err = s.reconciler.Reconcile(s.Context(), req1)
	s.Require().NoError(err)

	// Create second HTTPRoute with same hostname
	httpRoute2 := testutil.NewTestHTTPRoute("route-2", s.testNamespace, testutil.TestGatewayName, testutil.TestHostname)
	err = s.Client().Create(s.Context(), httpRoute2)
	s.Require().NoError(err)

	// Setup mock expectations for second route (should find existing resource)
	existingResources := []map[string]interface{}{
		{
			"resourceId": "resource-123",
			"name":       testutil.TestHostname,
		},
	}

	target2 := map[string]interface{}{
		"targetId": float64(457),
		"ip":       service.Spec.ClusterIP,
		"port":     float64(80),
		"path":     "/",
	}

	s.mockPangolin.On("ListResources", s.Context()).Return(existingResources, nil).Once()
	s.mockPangolin.On("ListTargetsRaw", s.Context(), "resource-123").Return([]map[string]interface{}{target1}, nil).Once()
	s.mockPangolin.On("CreateTargetRaw", s.Context(), "resource-123", testutil.MockAnything).Return(target2, nil).Once()

	// Reconcile second route
	req2 := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "route-2",
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req2)
	s.Require().NoError(err)
	s.Require().Equal(5*time.Minute, result.RequeueAfter)

	// Both routes should be accepted and share the same resource
	s.Eventually(func() bool {
		fresh1 := &gatewayv1.HTTPRoute{}
		fresh2 := &gatewayv1.HTTPRoute{}

		err1 := s.Client().Get(s.Context(), client.ObjectKeyFromObject(httpRoute1), fresh1)
		err2 := s.Client().Get(s.Context(), client.ObjectKeyFromObject(httpRoute2), fresh2)

		if err1 != nil || err2 != nil {
			return false
		}

		// Both should be accepted
		accepted1, accepted2 := false, false
		for _, status := range fresh1.Status.RouteStatus.Parents {
			for _, cond := range status.Conditions {
				if cond.Type == string(gatewayv1.RouteConditionAccepted) && cond.Status == metav1.ConditionTrue {
					accepted1 = true
				}
			}
		}
		for _, status := range fresh2.Status.RouteStatus.Parents {
			for _, cond := range status.Conditions {
				if cond.Type == string(gatewayv1.RouteConditionAccepted) && cond.Status == metav1.ConditionTrue {
					accepted2 = true
				}
			}
		}

		return accepted1 && accepted2
	}, "Both HTTPRoutes should be accepted and share the resource")
}

// TestSuite runs the HTTPRoute controller test suite.
func TestHTTPRouteControllerSuite(t *testing.T) {
	suite.Run(t, new(HTTPRouteControllerTestSuite))
}
