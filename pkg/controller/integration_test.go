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

// IntegrationTestSuite tests all 5 controllers working together.
type IntegrationTestSuite struct {
	testutil.EnvTestSuite
	gatewayReconciler   *controller.GatewayReconciler
	newtReconciler      *controller.NewtReconciler
	httpRouteReconciler *controller.HTTPRouteReconciler
	grpcRouteReconciler *controller.GRPCRouteReconciler
	mockPangolin        *testutil.MockPangolinClient
	testNamespace       string
	eventRecorder       record.EventRecorder
}

// SetupSuite runs once before all tests.
func (s *IntegrationTestSuite) SetupSuite() {
	s.EnvTestSuite.SetupSuite()
	s.testNamespace = testutil.TestNamespace

	// Create namespace + GatewayClass once for the entire suite
	ns := testutil.NewTestNamespace(s.testNamespace)
	err := s.Client().Create(s.Context(), ns)
	s.Require().NoError(err)

	gatewayClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: testutil.TestGatewayClass,
		},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: "pangol.in/gateway-controller",
		},
	}
	err = s.Client().Create(s.Context(), gatewayClass)
	s.Require().NoError(err)
}

// SetupTest runs before each test.
func (s *IntegrationTestSuite) SetupTest() {
	// Create fresh mock for each test
	s.mockPangolin = testutil.NewMockPangolinClient()

	// Create event recorder
	s.eventRecorder = record.NewFakeRecorder(100)

	// Create controller config
	cfg := &config.ControllerConfig{
		GatewayClassName: testutil.TestGatewayClass,
		NewtImage:        "docker.io/fosrl/newt:1.10.0",
		NewtEndpoint:     "https://api.pangolin.net",
	}

	// Create all 5 reconcilers
	s.gatewayReconciler = &controller.GatewayReconciler{
		Client:          s.Client(),
		Log:             ctrl.Log.WithName("test").WithName("Gateway"),
		Scheme:          s.Client().Scheme(),
		PangolinClient:  s.mockPangolin,
		ControllerClass: testutil.TestGatewayClass,
		Recorder:        s.eventRecorder,
	}

	s.newtReconciler = &controller.NewtReconciler{
		Client:          s.Client(),
		Log:             ctrl.Log.WithName("test").WithName("Newt"),
		Scheme:          s.Client().Scheme(),
		Config:          cfg,
		NewtImage:       controller.NewtImage,
		ControllerClass: testutil.TestGatewayClass,
	}

	s.httpRouteReconciler = &controller.HTTPRouteReconciler{
		Client:         s.Client(),
		Log:            ctrl.Log.WithName("test").WithName("HTTPRoute"),
		Scheme:         s.Client().Scheme(),
		PangolinClient: s.mockPangolin,
		Config:         cfg,
		Recorder:       s.eventRecorder,
	}

	s.grpcRouteReconciler = &controller.GRPCRouteReconciler{
		Client:         s.Client(),
		Log:            ctrl.Log.WithName("test").WithName("GRPCRoute"),
		Scheme:         s.Client().Scheme(),
		PangolinClient: s.mockPangolin,
		Config:         cfg,
		Recorder:       s.eventRecorder,
	}
}

// TearDownTest runs after each test.
func (s *IntegrationTestSuite) TearDownTest() {
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

	// Strip finalizers from GRPCRoutes so they can be deleted immediately
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

	// Strip finalizers from Gateways so they can be deleted immediately
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
	_ = s.Client().DeleteAllOf(ctx, &gatewayv1.GRPCRoute{}, ns)
	_ = s.Client().DeleteAllOf(ctx, &gatewayv1.Gateway{}, ns)

	// Wait until all Gateways are gone before the next test's SetupTest creates them
	s.Require().Eventually(func() bool {
		var list gatewayv1.GatewayList
		_ = s.Client().List(ctx, &list, ns)
		return len(list.Items) == 0
	}, 30*time.Second, 100*time.Millisecond, "gateways should be deleted")

	// Verify all mock expectations
	s.mockPangolin.AssertExpectations(s.T())
}

// TestEndToEnd_GatewayCreation tests the full workflow: Gateway -> Site -> Secret -> Newt deployment.
func (s *IntegrationTestSuite) TestEndToEnd_GatewayCreation() {
	gateway := testutil.NewTestGateway(testutil.TestGatewayName, s.testNamespace)

	// Create Gateway in cluster
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// Setup mock expectations for Gateway reconciler
	siteDefaults := &pangolin.SiteDefaults{
		ExitNodeID:    1,
		Subnet:        "10.0.0.0/24",
		ClientAddress: "10.0.0.1",
		NewtID:        "newt-123",
		NewtSecret:    "secret-456",
		PublicKey:     "pubkey",
		Endpoint:      "https://api.pangolin.net",
		ListenPort:    51820,
	}

	createdSite := &pangolin.Site{
		ID:         12345,
		Name:       "pgc-" + testutil.TestGatewayName,
		Type:       "newt",
		Subnet:     siteDefaults.Subnet,
		ExitNodeID: siteDefaults.ExitNodeID,
	}

	s.mockPangolin.On("ListSites", testutil.MockAnything).Return([]pangolin.Site{}, nil).Once()
	s.mockPangolin.On("PickSiteDefaults", testutil.MockAnything).Return(siteDefaults, nil).Once()
	s.mockPangolin.On("CreateSite", testutil.MockAnything, testutil.MockAnything).Return(createdSite, nil).Once()

	// Step 1: Reconcile Gateway to create Site and Secret
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testutil.TestGatewayName,
			Namespace: s.testNamespace,
		},
	}

	result, err := s.gatewayReconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)

	// Verify Gateway was updated with site ID
	s.Eventually(func() bool {
		fresh := &gatewayv1.Gateway{}
		err := s.Client().Get(s.Context(), client.ObjectKeyFromObject(gateway), fresh)
		if err != nil {
			return false
		}

		siteID, exists := fresh.Labels[controller.SiteIDLabel]
		return exists && siteID == "12345"
	}, "Gateway should have site ID label")

	// Verify Secret was created
	s.Eventually(func() bool {
		secret := &corev1.Secret{}
		err := s.Client().Get(s.Context(), types.NamespacedName{
			Name:      testutil.TestGatewayName + "-newt-cred",
			Namespace: s.testNamespace,
		}, secret)
		return err == nil
	}, "Secret should be created")

	// Step 2: Reconcile Newt to create Deployment
	result, err = s.newtReconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)

	// Verify Newt Deployment was created
	s.Eventually(func() bool {
		deployment := testutil.NewTestDeployment(testutil.TestGatewayName+"-newt", s.testNamespace)
		err := s.Client().Get(s.Context(), client.ObjectKeyFromObject(deployment), deployment)
		return err == nil
	}, "Newt Deployment should be created")

	// Verify Newt Service was created
	s.Eventually(func() bool {
		svc := &corev1.Service{}
		err := s.Client().Get(s.Context(), types.NamespacedName{
			Name:      testutil.TestGatewayName + "-newt",
			Namespace: s.testNamespace,
		}, svc)
		return err == nil
	}, "Newt Service should be created")
}

// TestEndToEnd_HTTPRouteReconciliation tests HTTPRoute creation and target reconciliation.
func (s *IntegrationTestSuite) TestEndToEnd_HTTPRouteReconciliation() {
	// Create Gateway first
	gateway := testutil.NewTestGateway(testutil.TestGatewayName, s.testNamespace)
	gateway.Labels = map[string]string{
		controller.SiteIDLabel: "12345",
	}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// Create backend Service
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testutil.TestServiceName,
			Namespace: s.testNamespace,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port: 80,
				},
			},
		},
	}
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
		"ip":       "10.96.0.1",
		"port":     float64(80),
	}

	s.mockPangolin.On("ListDomains", testutil.MockAnything).Return(domains, nil).Maybe()
	s.mockPangolin.On("ListResources", testutil.MockAnything).Return([]map[string]interface{}{}, nil).Once()
	s.mockPangolin.On("CreateResource", testutil.MockAnything, testutil.MockAnything).Return(resource, nil).Once()
	s.mockPangolin.On("UpdateResource", testutil.MockAnything, "resource-123", testutil.MockAnything).Return(nil).Maybe()
	s.mockPangolin.On("DisableSSO", testutil.MockAnything, "resource-123").Return(nil).Maybe()
	s.mockPangolin.On("ListTargetsRaw", testutil.MockAnything, "resource-123").Return([]map[string]interface{}{}, nil).Once()
	s.mockPangolin.On("CreateTargetRaw", testutil.MockAnything, "resource-123", testutil.MockAnything).Return(target, nil).Once()

	// Reconcile HTTPRoute
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testutil.TestHTTPRouteName,
			Namespace: s.testNamespace,
		},
	}

	result, err := s.httpRouteReconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().Equal(5*time.Minute, result.RequeueAfter, "Should requeue after 5 minutes")

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

// TestEndToEnd_GatewayDeletion tests Gateway deletion triggers finalizer cleanup.
func (s *IntegrationTestSuite) TestEndToEnd_GatewayDeletion() {
	gateway := testutil.NewTestGateway(testutil.TestGatewayName, s.testNamespace)
	gateway.Labels = map[string]string{
		controller.SiteIDLabel: "12345",
	}
	gateway.Finalizers = []string{controller.FinalizerName}

	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// Mock expects delete call
	s.mockPangolin.On("DeleteSite", testutil.MockAnything, 12345).Return(nil).Once()

	// Delete Gateway
	err = s.Client().Delete(s.Context(), gateway)
	s.Require().NoError(err)

	// Reconcile to process deletion
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testutil.TestGatewayName,
			Namespace: s.testNamespace,
		},
	}

	result, err := s.gatewayReconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)

	// Verify Gateway was deleted (finalizer removed)
	s.Eventually(func() bool {
		fresh := &gatewayv1.Gateway{}
		err := s.Client().Get(s.Context(), client.ObjectKeyFromObject(gateway), fresh)
		return err != nil // Gateway should not exist
	}, "Gateway should be deleted")
}

// TestEndToEnd_GRPCRouteReconciliation tests GRPCRoute creation for TCP/UDP services.
func (s *IntegrationTestSuite) TestEndToEnd_GRPCRouteReconciliation() {
	// Create Gateway first
	gateway := testutil.NewTestGateway(testutil.TestGatewayName, s.testNamespace)
	gateway.Labels = map[string]string{
		controller.SiteIDLabel: "12345",
	}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// Create backend Service
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testutil.TestServiceName,
			Namespace: s.testNamespace,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port: 5432,
				},
			},
		},
	}
	err = s.Client().Create(s.Context(), service)
	s.Require().NoError(err)

	// Create GRPCRoute
	grpcRoute := testutil.NewTestGRPCRoute(testutil.TestGRPCRouteName, s.testNamespace, testutil.TestGatewayName, testutil.TestHostname)
	err = s.Client().Create(s.Context(), grpcRoute)
	s.Require().NoError(err)

	// Setup mock expectations
	domains := []map[string]interface{}{
		{
			"name":     "example.com",
			"domainId": "domain1",
		},
	}

	resource := map[string]interface{}{
		"resourceId": "resource-789",
		"subdomain":  "test-namespace-test-grpcroute",
		"http":       false,
		"protocol":   "tcp",
	}

	target := map[string]interface{}{
		"targetId": float64(789),
		"ip":       "10.96.0.2",
		"port":     float64(5432),
	}

	s.mockPangolin.On("ListDomains", testutil.MockAnything).Return(domains, nil).Maybe()
	s.mockPangolin.On("CreateResource", testutil.MockAnything, testutil.MockAnything).Return(resource, nil).Once()
	s.mockPangolin.On("ListTargetsRaw", testutil.MockAnything, "resource-789").Return([]map[string]interface{}{}, nil).Once()
	s.mockPangolin.On("CreateTargetRaw", testutil.MockAnything, "resource-789", testutil.MockAnything).Return(target, nil).Once()

	// Reconcile GRPCRoute
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testutil.TestGRPCRouteName,
			Namespace: s.testNamespace,
		},
	}

	result, err := s.grpcRouteReconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().Equal(5*time.Minute, result.RequeueAfter)

	// Verify GRPCRoute was updated with resource ID
	s.Eventually(func() bool {
		fresh := &gatewayv1.GRPCRoute{}
		err := s.Client().Get(s.Context(), client.ObjectKeyFromObject(grpcRoute), fresh)
		if err != nil {
			return false
		}

		resourceID, exists := fresh.Labels[controller.ResourceIDLabel]
		return exists && resourceID == "resource-789"
	}, "GRPCRoute should have resource ID label")
}

// TestSuite runs the integration test suite.
func TestIntegrationSuite(t *testing.T) {
	suite.Run(t, new(IntegrationTestSuite))
}
