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
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// TCPRouteControllerTestSuite exercises the TCPRouteReconciler against a real
// envtest API server, verifying Pangolin resource lifecycle and status updates.
type TCPRouteControllerTestSuite struct {
	testutil.EnvTestSuite
	reconciler    *controller.TCPRouteReconciler
	mockPangolin  *testutil.MockPangolinClient
	testNamespace string
}

func (s *TCPRouteControllerTestSuite) SetupSuite() {
	s.EnvTestSuite.SetupSuite()
	s.testNamespace = "tcproute-test-ns"

	ns := testutil.NewTestNamespace(s.testNamespace)
	err := s.Client().Create(s.Context(), ns)
	s.Require().NoError(err)
}

func (s *TCPRouteControllerTestSuite) SetupTest() {
	s.mockPangolin = testutil.NewMockPangolinClient()

	cfg := &config.ControllerConfig{
		GatewayClassName: testutil.TestGatewayClass,
	}

	s.reconciler = &controller.TCPRouteReconciler{
		Client:         s.Client(),
		Log:            ctrl.Log.WithName("test").WithName("TCPRoute"),
		Scheme:         s.Client().Scheme(),
		PangolinClient: s.mockPangolin,
		Config:         cfg,
		Recorder:       record.NewFakeRecorder(100),
	}
}

func (s *TCPRouteControllerTestSuite) TearDownTest() {
	ctx := s.Context()
	ns := client.InNamespace(s.testNamespace)

	var tcpRoutes gatewayv1alpha2.TCPRouteList
	_ = s.Client().List(ctx, &tcpRoutes, ns)
	for i := range tcpRoutes.Items {
		route := &tcpRoutes.Items[i]
		route.Finalizers = nil
		_ = s.Client().Update(ctx, route)
		_ = s.Client().Delete(ctx, route)
	}

	var gateways gatewayv1.GatewayList
	_ = s.Client().List(ctx, &gateways, ns)
	for i := range gateways.Items {
		_ = s.Client().Delete(ctx, &gateways.Items[i])
	}

	var services corev1.ServiceList
	_ = s.Client().List(ctx, &services, ns)
	for i := range services.Items {
		_ = s.Client().Delete(ctx, &services.Items[i])
	}
}

// TestReconcile_HappyPath verifies that when a Gateway with a valid site ID
// exists, the reconciler creates a Pangolin resource and target, and sets the
// route status to Accepted.
func (s *TCPRouteControllerTestSuite) TestReconcile_HappyPath() {
	gateway := testutil.NewTestGateway("tcp-gw-happy", s.testNamespace)
	gateway.Labels = map[string]string{controller.SiteIDLabel: testutil.TestSiteID}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	port := gatewayv1.PortNumber(9000)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "tcp-backend", Namespace: s.testNamespace},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.96.55.1",
			Ports: []corev1.ServicePort{
				{Port: 9000, Protocol: corev1.ProtocolTCP},
			},
		},
	}
	err = s.Client().Create(s.Context(), svc)
	s.Require().NoError(err)

	route := &gatewayv1alpha2.TCPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tcp-route-happy",
			Namespace: s.testNamespace,
		},
		Spec: gatewayv1alpha2.TCPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: "tcp-gw-happy"}},
			},
			Rules: []gatewayv1alpha2.TCPRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: "tcp-backend",
						Port: &port,
					},
				}},
			}},
		},
	}
	err = s.Client().Create(s.Context(), route)
	s.Require().NoError(err)

	// Expect resource + target creation
	s.mockPangolin.On("CreateResource", testutil.MockAnything, testutil.MockAnything).
		Return(map[string]any{"resourceId": "tcp-res-1"}, nil).Once()
	s.mockPangolin.On("ListTargetsRaw", testutil.MockAnything, "tcp-res-1").
		Return([]map[string]any{}, nil).Once()
	s.mockPangolin.On("CreateTargetRaw", testutil.MockAnything, "tcp-res-1", testutil.MockAnything).
		Return(map[string]any{"targetId": "tcp-tgt-1"}, nil).Once()

	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name: "tcp-route-happy", Namespace: s.testNamespace,
	}}
	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().Equal(5*time.Minute, result.RequeueAfter)

	s.mockPangolin.AssertExpectations(s.T())

	// Verify route status is Accepted
	s.Eventually(func() bool {
		fresh := &gatewayv1alpha2.TCPRoute{}
		if err := s.Client().Get(s.Context(),
			types.NamespacedName{Name: "tcp-route-happy", Namespace: s.testNamespace},
			fresh); err != nil {
			return false
		}
		for _, ps := range fresh.Status.Parents {
			for _, c := range ps.Conditions {
				if c.Type == string(gatewayv1.RouteConditionAccepted) && c.Status == metav1.ConditionTrue {
					return true
				}
			}
		}
		return false
	}, 5*time.Second, 100*time.Millisecond, "TCPRoute must be Accepted after successful reconcile")
}

// TestReconcile_NoParentGateway verifies that when the route has no parentRefs
// the controller sets status NoParentGateway and requeues after 30s.
func (s *TCPRouteControllerTestSuite) TestReconcile_NoParentGateway() {
	route := &gatewayv1alpha2.TCPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tcp-route-no-parent",
			Namespace: s.testNamespace,
		},
		Spec: gatewayv1alpha2.TCPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{},
			},
			Rules: []gatewayv1alpha2.TCPRouteRule{},
		},
	}
	err := s.Client().Create(s.Context(), route)
	s.Require().NoError(err)

	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name: "tcp-route-no-parent", Namespace: s.testNamespace,
	}}
	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().Equal(30*time.Second, result.RequeueAfter,
		"should requeue after 30s when no parent refs")
}

// TestReconcile_GatewayNotReady verifies that when the parent Gateway has no
// site ID label yet (not yet provisioned), the controller requeues after 10s.
func (s *TCPRouteControllerTestSuite) TestReconcile_GatewayNotReady() {
	gateway := testutil.NewTestGateway("tcp-gw-not-ready", s.testNamespace)
	// Intentionally NO SiteIDLabel
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	route := &gatewayv1alpha2.TCPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "tcp-route-gw-not-ready",
			Namespace: s.testNamespace,
		},
		Spec: gatewayv1alpha2.TCPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: "tcp-gw-not-ready"}},
			},
			Rules: []gatewayv1alpha2.TCPRouteRule{},
		},
	}
	err = s.Client().Create(s.Context(), route)
	s.Require().NoError(err)

	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name: "tcp-route-gw-not-ready", Namespace: s.testNamespace,
	}}
	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().Equal(10*time.Second, result.RequeueAfter,
		"should requeue after 10s while gateway is not ready")
}

// TestReconcile_Delete verifies that when a TCPRoute is being deleted, the
// reconciler removes the Pangolin resource and strips the finalizer so the
// object can be garbage-collected by Kubernetes.
func (s *TCPRouteControllerTestSuite) TestReconcile_Delete() {
	gateway := testutil.NewTestGateway("tcp-gw-del", s.testNamespace)
	gateway.Labels = map[string]string{controller.SiteIDLabel: testutil.TestSiteID}
	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	route := &gatewayv1alpha2.TCPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "tcp-route-del",
			Namespace:  s.testNamespace,
			Finalizers: []string{controller.FinalizerName},
			Labels:     map[string]string{controller.ResourceIDLabel: "tcp-res-del"},
		},
		Spec: gatewayv1alpha2.TCPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{Name: "tcp-gw-del"}},
			},
			Rules: []gatewayv1alpha2.TCPRouteRule{},
		},
	}
	err = s.Client().Create(s.Context(), route)
	s.Require().NoError(err)

	err = s.Client().Delete(s.Context(), route)
	s.Require().NoError(err)

	s.mockPangolin.On("DeleteResource", testutil.MockAnything, "tcp-res-del").
		Return(nil).Once()

	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name: "tcp-route-del", Namespace: s.testNamespace,
	}}
	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().Zero(result.RequeueAfter)

	s.mockPangolin.AssertExpectations(s.T())

	// Finalizer removed → object should be gone
	s.Eventually(func() bool {
		fresh := &gatewayv1alpha2.TCPRoute{}
		return s.Client().Get(s.Context(),
			types.NamespacedName{Name: "tcp-route-del", Namespace: s.testNamespace},
			fresh) != nil // NotFound = finalizer removed
	}, 5*time.Second, 100*time.Millisecond, "finalizer must be removed on deletion")
}

func TestTCPRouteControllerSuite(t *testing.T) {
	suite.Run(t, new(TCPRouteControllerTestSuite))
}
