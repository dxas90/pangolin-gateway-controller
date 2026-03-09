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

	// Create namespace once for the entire suite
	ns := testutil.NewTestNamespace(s.testNamespace)
	err := s.Client().Create(s.Context(), ns)
	s.Require().NoError(err)
}

// SetupTest runs before each test.
func (s *HTTPRouteControllerTestSuite) SetupTest() {
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
	ctx := s.Context()
	ns := client.InNamespace(s.testNamespace)

	// Strip finalizers from HTTPRoutes so they can be deleted immediately
	var httpRoutes gatewayv1.HTTPRouteList
	_ = s.Client().List(ctx, &httpRoutes, ns)
	for i := range httpRoutes.Items {
		hr := httpRoutes.Items[i].DeepCopy()
		if len(hr.Finalizers) > 0 {
			patch := client.MergeFrom(hr.DeepCopy())
			hr.Finalizers = nil
			_ = s.Client().Patch(ctx, hr, patch)
		}
	}

	// Strip finalizers from Gateways
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
	_ = s.Client().DeleteAllOf(ctx, &gatewayv1.HTTPRoute{}, ns)
	_ = s.Client().DeleteAllOf(ctx, &gatewayv1.Gateway{}, ns)

	// Wait until Gateways and HTTPRoutes are gone
	s.Require().Eventually(func() bool {
		var gwList gatewayv1.GatewayList
		var hrList gatewayv1.HTTPRouteList
		_ = s.Client().List(ctx, &gwList, ns)
		_ = s.Client().List(ctx, &hrList, ns)
		return len(gwList.Items) == 0 && len(hrList.Items) == 0
	}, 30*time.Second, 100*time.Millisecond, "gateways and httproutes should be deleted")

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

	s.mockPangolin.On("ListDomains", testutil.MockAnything).Return(domains, nil).Maybe()
	s.mockPangolin.On("ListResources", testutil.MockAnything).Return([]map[string]interface{}{}, nil).Once()
	s.mockPangolin.On("CreateResource", testutil.MockAnything, testutil.MockAnything).Return(resource, nil).Once()
	s.mockPangolin.On("UpdateResource", testutil.MockAnything, "resource-123", testutil.MockAnything).Return(nil).Maybe()
	s.mockPangolin.On("DisableSSO", testutil.MockAnything, "resource-123").Return(nil).Maybe()
	s.mockPangolin.On("ListTargetsRaw", testutil.MockAnything, "resource-123").Return([]map[string]interface{}{}, nil).Once()
	s.mockPangolin.On("CreateTargetRaw", testutil.MockAnything, "resource-123", testutil.MockAnything).Return(target, nil).Once()

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

	s.mockPangolin.On("ListResources", testutil.MockAnything).Return(resources, nil).Once()
	s.mockPangolin.On("ListTargetsRaw", testutil.MockAnything, "resource-123").Return(targets, nil).Once()
	s.mockPangolin.On("DeleteTarget", testutil.MockAnything, "456").Return(nil).Once()
	s.mockPangolin.On("DeleteResource", testutil.MockAnything, "resource-123").Return(nil).Maybe()

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
	// Re-fetch to get server-assigned ClusterIP
	err = s.Client().Get(s.Context(), client.ObjectKeyFromObject(service), service)
	s.Require().NoError(err)

	// Create first HTTPRoute with path /api
	httpRoute1 := testutil.NewTestHTTPRoute("route-1", s.testNamespace, testutil.TestGatewayName, testutil.TestHostname)
	apiPath := "/api"
	apiPathType := gatewayv1.PathMatchPathPrefix
	httpRoute1.Spec.Rules[0].Matches = []gatewayv1.HTTPRouteMatch{
		{Path: &gatewayv1.HTTPPathMatch{Type: &apiPathType, Value: &apiPath}},
	}
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
		"path":     "/api",
		"siteId":   float64(12345),
	}

	s.mockPangolin.On("ListDomains", testutil.MockAnything).Return(domains, nil).Maybe()
	s.mockPangolin.On("ListResources", testutil.MockAnything).Return([]map[string]interface{}{}, nil).Once()
	s.mockPangolin.On("CreateResource", testutil.MockAnything, testutil.MockAnything).Return(resource, nil).Once()
	s.mockPangolin.On("UpdateResource", testutil.MockAnything, "resource-123", testutil.MockAnything).Return(nil).Maybe()
	s.mockPangolin.On("DisableSSO", testutil.MockAnything, "resource-123").Return(nil).Maybe()
	s.mockPangolin.On("ListTargetsRaw", testutil.MockAnything, "resource-123").Return([]map[string]interface{}{}, nil).Once()
	s.mockPangolin.On("CreateTargetRaw", testutil.MockAnything, "resource-123", testutil.MockAnything).Return(target1, nil).Once()

	// Reconcile first route
	req1 := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "route-1",
			Namespace: s.testNamespace,
		},
	}

	_, err = s.reconciler.Reconcile(s.Context(), req1)
	s.Require().NoError(err)

	// Create second HTTPRoute with same hostname but different path /extra
	httpRoute2 := testutil.NewTestHTTPRoute("route-2", s.testNamespace, testutil.TestGatewayName, testutil.TestHostname)
	extraPath := "/extra"
	extraPathType := gatewayv1.PathMatchPathPrefix
	httpRoute2.Spec.Rules[0].Matches = []gatewayv1.HTTPRouteMatch{
		{Path: &gatewayv1.HTTPPathMatch{Type: &extraPathType, Value: &extraPath}},
	}
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
		"path":     "/extra",
		"siteId":   float64(12345),
	}

	s.mockPangolin.On("ListResources", testutil.MockAnything).Return(existingResources, nil).Once()
	// route-2 gets existing targets including target1 for /api (owned by route-1, different path)
	s.mockPangolin.On("ListTargetsRaw", testutil.MockAnything, "resource-123").Return([]map[string]interface{}{target1}, nil).Once()
	// route-2 needs to create a new target for its /extra path
	s.mockPangolin.On("CreateTargetRaw", testutil.MockAnything, "resource-123", testutil.MockAnything).Return(target2, nil).Once()

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

// TestReconcile_MultipleBackends verifies that all BackendRefs in a rule create separate targets.
func (s *HTTPRouteControllerTestSuite) TestReconcile_MultipleBackends() {
	// Create Gateway
	gateway := testutil.NewTestGateway(testutil.TestGatewayName, s.testNamespace)
	gateway.Labels = map[string]string{
		controller.SiteIDLabel: testutil.TestSiteID,
	}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// Create two backend Services with distinct IPs (envtest assigns ClusterIPs)
	svcA := testutil.NewTestService("service-a", s.testNamespace)
	svcA.Spec.Ports[0].Port = 80
	err = s.Client().Create(s.Context(), svcA)
	s.Require().NoError(err)
	err = s.Client().Get(s.Context(), client.ObjectKeyFromObject(svcA), svcA)
	s.Require().NoError(err)

	svcB := testutil.NewTestService("service-b", s.testNamespace)
	svcB.Spec.Ports[0].Port = 9090
	err = s.Client().Create(s.Context(), svcB)
	s.Require().NoError(err)
	err = s.Client().Get(s.Context(), client.ObjectKeyFromObject(svcB), svcB)
	s.Require().NoError(err)

	// Build HTTPRoute with 1 rule, 2 backends
	httpRoute := testutil.NewTestHTTPRoute("multi-backend-route", s.testNamespace, testutil.TestGatewayName, testutil.TestHostname)
	port80 := gatewayv1.PortNumber(80)
	port9090 := gatewayv1.PortNumber(9090)
	httpRoute.Spec.Rules = []gatewayv1.HTTPRouteRule{
		{
			BackendRefs: []gatewayv1.HTTPBackendRef{
				{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "service-a",
							Port: &port80,
						},
					},
				},
				{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: "service-b",
							Port: &port9090,
						},
					},
				},
			},
		},
	}
	err = s.Client().Create(s.Context(), httpRoute)
	s.Require().NoError(err)

	// Mock Pangolin API
	domains := []map[string]interface{}{{"baseDomain": "example.com", "domainId": "domain1"}}
	resource := map[string]interface{}{"resourceId": "resource-multi", "name": testutil.TestHostname}
	targetA := map[string]interface{}{"targetId": float64(100), "ip": svcA.Spec.ClusterIP, "port": float64(80), "path": "/"}
	targetB := map[string]interface{}{"targetId": float64(101), "ip": svcB.Spec.ClusterIP, "port": float64(9090), "path": "/"}

	s.mockPangolin.On("ListDomains", testutil.MockAnything).Return(domains, nil).Maybe()
	s.mockPangolin.On("ListResources", testutil.MockAnything).Return([]map[string]interface{}{}, nil).Once()
	s.mockPangolin.On("CreateResource", testutil.MockAnything, testutil.MockAnything).Return(resource, nil).Once()
	s.mockPangolin.On("UpdateResource", testutil.MockAnything, "resource-multi", testutil.MockAnything).Return(nil).Maybe()
	s.mockPangolin.On("DisableSSO", testutil.MockAnything, "resource-multi").Return(nil).Maybe()
	s.mockPangolin.On("ListTargetsRaw", testutil.MockAnything, "resource-multi").Return([]map[string]interface{}{}, nil).Once()
	// Expect two CreateTargetRaw calls — one per backend
	s.mockPangolin.On("CreateTargetRaw", testutil.MockAnything, "resource-multi", testutil.MockAnything).Return(targetA, nil).Once()
	s.mockPangolin.On("CreateTargetRaw", testutil.MockAnything, "resource-multi", testutil.MockAnything).Return(targetB, nil).Once()

	// Reconcile
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "multi-backend-route",
			Namespace: s.testNamespace,
		},
	}
	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().Equal(5*time.Minute, result.RequeueAfter, "Should requeue after 5 minutes")

	// Both target calls must have been made (verified in TearDownTest via AssertExpectations)
}

// TestSuite runs the HTTPRoute controller test suite.
func TestHTTPRouteControllerSuite(t *testing.T) {
	suite.Run(t, new(HTTPRouteControllerTestSuite))
}
