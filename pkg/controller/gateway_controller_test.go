package controller_test

import (
	"testing"
	"time"

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

// GatewayControllerTestSuite tests the GatewayReconciler.
type GatewayControllerTestSuite struct {
	testutil.EnvTestSuite
	reconciler    *controller.GatewayReconciler
	mockPangolin  *testutil.MockPangolinClient
	testNamespace string
}

// SetupSuite runs once before all tests.
func (s *GatewayControllerTestSuite) SetupSuite() {
	s.EnvTestSuite.SetupSuite()
	s.testNamespace = testutil.TestNamespace

	// Create namespace once for the entire suite
	ns := testutil.NewTestNamespace(s.testNamespace)
	err := s.Client().Create(s.Context(), ns)
	s.Require().NoError(err)
}

// SetupTest runs before each test.
func (s *GatewayControllerTestSuite) SetupTest() {
	// Create fresh mock for each test
	s.mockPangolin = testutil.NewMockPangolinClient()

	// Create reconciler with mock client
	s.reconciler = &controller.GatewayReconciler{
		Client:          s.Client(),
		Log:             ctrl.Log.WithName("test").WithName("Gateway"),
		Scheme:          s.Client().Scheme(),
		PangolinClient:  s.mockPangolin,
		ControllerClass: testutil.TestGatewayClass,
		Recorder:        record.NewFakeRecorder(100),
	}
}

// TearDownTest runs after each test.
func (s *GatewayControllerTestSuite) TearDownTest() {
	ctx := s.Context()
	ns := client.InNamespace(s.testNamespace)

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
	_ = s.Client().DeleteAllOf(ctx, &gatewayv1.Gateway{}, ns)

	// Wait until Gateways are gone
	s.Require().Eventually(func() bool {
		var list gatewayv1.GatewayList
		_ = s.Client().List(ctx, &list, ns)
		return len(list.Items) == 0
	}, 30*time.Second, 100*time.Millisecond, "gateways should be deleted")

	// Verify all mock expectations
	s.mockPangolin.AssertExpectations(s.T())
}

// TestReconcile_NewGateway tests reconciling a new Gateway.
func (s *GatewayControllerTestSuite) TestReconcile_NewGateway() {
	gateway := testutil.NewTestGateway(testutil.TestGatewayName, s.testNamespace)

	// Create Gateway in cluster
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// Setup mock expectations
	siteDefaults := &pangolin.SiteDefaults{
		ExitNodeID:    1,
		Subnet:        "10.0.0.0/24",
		ClientAddress: "10.0.0.1",
		NewtID:        "newt-123",
		NewtSecret:    "secret-456",
		PublicKey:     "pubkey",
		Endpoint:      "https://pangolin.example.com",
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

	// Reconcile
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testutil.TestGatewayName,
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)

	// Verify Gateway was updated
	s.Eventually(func() bool {
		fresh := &gatewayv1.Gateway{}
		err := s.Client().Get(s.Context(), client.ObjectKeyFromObject(gateway), fresh)
		if err != nil {
			return false
		}

		// Check labels
		siteID, exists := fresh.Labels[controller.SiteIDLabel]
		if !exists || siteID != "12345" {
			return false
		}

		// Check status conditions
		for _, cond := range fresh.Status.Conditions {
			if cond.Type == string(gatewayv1.GatewayConditionProgrammed) {
				return cond.Status == metav1.ConditionTrue
			}
		}
		return false
	}, "Gateway should be programmed with site ID")

	// Verify Secret was created with credentials
	s.Eventually(func() bool {
		secret := &corev1.Secret{}
		err := s.Client().Get(s.Context(), types.NamespacedName{
			Name:      testutil.TestGatewayName + "-newt-cred",
			Namespace: s.testNamespace,
		}, secret)
		if err != nil {
			return false
		}

		newtID, hasNewtID := secret.Data["NEWT_ID"]
		newtSecret, hasSecret := secret.Data["NEWT_SECRET"]

		return hasNewtID && hasSecret &&
			string(newtID) == "newt-123" &&
			string(newtSecret) == "secret-456"
	}, "Secret should be created with newt credentials")
}

// TestReconcile_ExistingGateway tests reconciling an existing Gateway.
func (s *GatewayControllerTestSuite) TestReconcile_ExistingGateway() {
	gateway := testutil.NewTestGateway(testutil.TestGatewayName, s.testNamespace)
	gateway.Labels = map[string]string{
		controller.SiteIDLabel: "12345",
	}

	// Create Gateway with site ID already set
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// Mock expects verification call
	existingSite := pangolin.Site{
		ID:     12345,
		Name:   "pgc-" + testutil.TestGatewayName,
		Online: true,
	}
	s.mockPangolin.On("ListSites", testutil.MockAnything).Return([]pangolin.Site{existingSite}, nil).Once()

	// Reconcile
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testutil.TestGatewayName,
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().Equal(5*time.Minute, result.RequeueAfter, "Should requeue after 5 minutes for verification")
}

// TestReconcile_DeleteGateway tests Gateway deletion with finalizer.
func (s *GatewayControllerTestSuite) TestReconcile_DeleteGateway() {
	gateway := testutil.NewTestGateway(testutil.TestGatewayName, s.testNamespace)
	gateway.Labels = map[string]string{
		controller.SiteIDLabel: "12345",
	}
	gateway.Finalizers = []string{controller.FinalizerName}

	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// Mock expects delete call
	s.mockPangolin.On("DeleteSite", testutil.MockAnything, 12345).Return(nil).Once()

	// Delete the Gateway
	err = s.Client().Delete(s.Context(), gateway)
	s.Require().NoError(err)

	// Reconcile (finalizer cleanup)
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testutil.TestGatewayName,
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)

	// Verify Gateway is eventually deleted
	s.Eventually(func() bool {
		fresh := &gatewayv1.Gateway{}
		err := s.Client().Get(s.Context(), client.ObjectKeyFromObject(gateway), fresh)
		return client.IgnoreNotFound(err) == nil && err != nil // Should be NotFound
	}, "Gateway should be deleted after finalizer cleanup")
}

// TestReconcile_WrongGatewayClass tests skipping Gateway with different class.
func (s *GatewayControllerTestSuite) TestReconcile_WrongGatewayClass() {
	gateway := testutil.NewTestGateway(testutil.TestGatewayName, s.testNamespace)
	gateway.Spec.GatewayClassName = "other-class"

	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// Should not call Pangolin API
	// (No mock expectations set)

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testutil.TestGatewayName,
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)

	// Mock should have no calls
	s.mockPangolin.AssertExpectations(s.T())
}

// TestSuite runs the test suite.
func TestGatewayControllerSuite(t *testing.T) {
	suite.Run(t, new(GatewayControllerTestSuite))
}
