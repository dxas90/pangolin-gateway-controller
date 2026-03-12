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

// NewtControllerTestSuite tests the NewtReconciler.
type NewtControllerTestSuite struct {
	testutil.EnvTestSuite
	reconciler    *controller.NewtReconciler
	mockPangolin  *testutil.MockPangolinClient
	testNamespace string
}

func (s *NewtControllerTestSuite) SetupSuite() {
	s.EnvTestSuite.SetupSuite()
	s.testNamespace = "newt-test-ns"

	ns := testutil.NewTestNamespace(s.testNamespace)
	err := s.Client().Create(s.Context(), ns)
	s.Require().NoError(err)
}

func (s *NewtControllerTestSuite) SetupTest() {
	s.mockPangolin = testutil.NewMockPangolinClient()

	cfg := &config.ControllerConfig{
		GatewayClassName: testutil.TestGatewayClass,
	}

	s.reconciler = &controller.NewtReconciler{
		Client:          s.Client(),
		Log:             ctrl.Log.WithName("test").WithName("Newt"),
		Scheme:          s.Client().Scheme(),
		PangolinClient:  s.mockPangolin,
		ControllerClass: testutil.TestGatewayClass,
		NewtImage:       controller.NewtImage,
		NewtEndpoint:    "https://pangolin.example.com",
		PangolinBaseURL: "https://api.example.com/v1",
		Config:          cfg,
		Recorder:        record.NewFakeRecorder(100),
	}
}

func (s *NewtControllerTestSuite) TearDownTest() {
	ctx := s.Context()
	ns := client.InNamespace(s.testNamespace)

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

	_ = s.Client().DeleteAllOf(ctx, &corev1.Secret{}, ns)
	_ = s.Client().DeleteAllOf(ctx, &gatewayv1.Gateway{}, ns)

	s.Require().Eventually(func() bool {
		var gwList gatewayv1.GatewayList
		_ = s.Client().List(ctx, &gwList, ns)
		return len(gwList.Items) == 0
	}, 30*time.Second, 100*time.Millisecond, "gateways should be deleted")

	s.mockPangolin.AssertExpectations(s.T())
}

// TestReconcile_NotFound tests reconciling a non-existent Gateway.
func (s *NewtControllerTestSuite) TestReconcile_NotFound() {
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "nonexistent-gateway",
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)
}

// TestReconcile_WrongGatewayClass tests skipping Gateway with different class.
func (s *NewtControllerTestSuite) TestReconcile_WrongGatewayClass() {
	gateway := testutil.NewTestGateway("newt-wrong-class", s.testNamespace)
	gateway.Spec.GatewayClassName = "other-class"

	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "newt-wrong-class",
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)
}

// TestReconcile_NoSiteID tests skipping Gateway without site ID label.
func (s *NewtControllerTestSuite) TestReconcile_NoSiteID() {
	gateway := testutil.NewTestGateway("newt-no-site", s.testNamespace)
	// No SiteIDLabel set

	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "newt-no-site",
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)
}

// TestReconcile_NoCredentialsSecret tests skipping when credentials secret doesn't exist.
func (s *NewtControllerTestSuite) TestReconcile_NoCredentialsSecret() {
	gateway := testutil.NewTestGateway("newt-no-secret", s.testNamespace)
	gateway.Labels = map[string]string{
		controller.SiteIDLabel: "12345",
	}

	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// No secret created — reconciler should return early
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "newt-no-secret",
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)
}

// TestReconcile_WithCredentials tests full newt deployment creation.
func (s *NewtControllerTestSuite) TestReconcile_WithCredentials() {
	gateway := testutil.NewTestGateway("newt-full", s.testNamespace)
	gateway.Labels = map[string]string{
		controller.SiteIDLabel: "12345",
	}

	err := s.Client().Create(s.Context(), gateway)
	s.Require().NoError(err)

	// Create the credentials secret that the newt reconciler reads
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "newt-full-newt-cred",
			Namespace: s.testNamespace,
		},
		Data: map[string][]byte{
			"NEWT_ID":     []byte("newt-123"),
			"NEWT_SECRET": []byte("secret-456"),
		},
	}
	err = s.Client().Create(s.Context(), secret)
	s.Require().NoError(err)

	site := &pangolin.Site{
		ID:     12345,
		NewtID: "newt-123",
		Secret: "secret-456",
	}
	_ = site // used via secret data

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "newt-full",
			Namespace: s.testNamespace,
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)
}

func TestNewtControllerSuite(t *testing.T) {
	suite.Run(t, new(NewtControllerTestSuite))
}
