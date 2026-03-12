package controller_test

import (
	"testing"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/config"
	"github.com/dxas90/pangolin-gateway-controller/pkg/controller"
	"github.com/dxas90/pangolin-gateway-controller/pkg/testutil"
	"github.com/stretchr/testify/suite"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// GatewayClassControllerTestSuite tests the GatewayClassReconciler.
type GatewayClassControllerTestSuite struct {
	testutil.EnvTestSuite
	reconciler *controller.GatewayClassReconciler
}

// SetupSuite runs once before all tests.
func (s *GatewayClassControllerTestSuite) SetupSuite() {
	s.EnvTestSuite.SetupSuite()
}

// SetupTest runs before each test.
func (s *GatewayClassControllerTestSuite) SetupTest() {
	cfg := &config.ControllerConfig{
		GatewayClassName:     testutil.TestGatewayClass,
		RateLimiterBaseDelay: 5 * time.Millisecond,
		RateLimiterMaxDelay:  1000 * time.Second,
		WorkqueueQPS:         10.0,
		WorkqueueBurst:       100,
	}

	s.reconciler = &controller.GatewayClassReconciler{
		Client:         s.Client(),
		Log:            ctrl.Log.WithName("test").WithName("GatewayClass"),
		Scheme:         s.Client().Scheme(),
		ControllerName: controller.ControllerName,
		Config:         cfg,
		Recorder:       record.NewFakeRecorder(100),
	}
}

// TearDownTest runs after each test.
func (s *GatewayClassControllerTestSuite) TearDownTest() {
	ctx := s.Context()
	var gcList gatewayv1.GatewayClassList
	_ = s.Client().List(ctx, &gcList)
	for i := range gcList.Items {
		_ = s.Client().Delete(ctx, &gcList.Items[i])
	}

	s.Require().Eventually(func() bool {
		var list gatewayv1.GatewayClassList
		_ = s.Client().List(ctx, &list)
		return len(list.Items) == 0
	}, 30*time.Second, 100*time.Millisecond, "gatewayclasses should be deleted")
}

// TestReconcile_AcceptGatewayClass tests accepting a GatewayClass managed by our controller.
func (s *GatewayClassControllerTestSuite) TestReconcile_AcceptGatewayClass() {
	gatewayClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pangolin-test",
		},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: gatewayv1.GatewayController(controller.ControllerName),
		},
	}

	err := s.Client().Create(s.Context(), gatewayClass)
	s.Require().NoError(err)

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: "pangolin-test",
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)

	// Verify status was updated to Accepted
	s.Eventually(func() bool {
		fresh := &gatewayv1.GatewayClass{}
		err := s.Client().Get(s.Context(), client.ObjectKeyFromObject(gatewayClass), fresh)
		if err != nil {
			return false
		}

		for _, cond := range fresh.Status.Conditions {
			if cond.Type == string(gatewayv1.GatewayClassConditionStatusAccepted) {
				return cond.Status == metav1.ConditionTrue
			}
		}
		return false
	}, "GatewayClass should be accepted")
}

// TestReconcile_SkipOtherController tests skipping a GatewayClass managed by another controller.
func (s *GatewayClassControllerTestSuite) TestReconcile_SkipOtherController() {
	gatewayClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "other-class",
		},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: "other.example.com/other-controller",
		},
	}

	err := s.Client().Create(s.Context(), gatewayClass)
	s.Require().NoError(err)

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: "other-class",
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)

	// Verify reconciler did not set Accepted=True (it should skip this GatewayClass)
	fresh := &gatewayv1.GatewayClass{}
	err = s.Client().Get(s.Context(), client.ObjectKeyFromObject(gatewayClass), fresh)
	s.Require().NoError(err)
	for _, cond := range fresh.Status.Conditions {
		if cond.Type == string(gatewayv1.GatewayClassConditionStatusAccepted) {
			s.Require().NotEqual(metav1.ConditionTrue, cond.Status, "GatewayClass should not be accepted by our controller")
		}
	}
}

// TestReconcile_NotFound tests reconciling a non-existent GatewayClass.
func (s *GatewayClassControllerTestSuite) TestReconcile_NotFound() {
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name: "does-not-exist",
		},
	}

	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)
}

// TestReconcile_IdempotentUpdate tests that re-reconciling doesn't update an already-accepted GatewayClass.
func (s *GatewayClassControllerTestSuite) TestReconcile_IdempotentUpdate() {
	gatewayClass := &gatewayv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "idempotent-class",
		},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: gatewayv1.GatewayController(controller.ControllerName),
		},
	}

	err := s.Client().Create(s.Context(), gatewayClass)
	s.Require().NoError(err)

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "idempotent-class"},
	}

	// First reconcile - sets accepted
	_, err = s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)

	// Wait for status to be set
	s.Eventually(func() bool {
		fresh := &gatewayv1.GatewayClass{}
		_ = s.Client().Get(s.Context(), types.NamespacedName{Name: "idempotent-class"}, fresh)
		for _, c := range fresh.Status.Conditions {
			if c.Type == string(gatewayv1.GatewayClassConditionStatusAccepted) &&
				c.Status == metav1.ConditionTrue {
				return true
			}
		}
		return false
	}, "GatewayClass should be accepted")

	// Second reconcile - should be idempotent (needsUpdate=false path)
	result, err := s.reconciler.Reconcile(s.Context(), req)
	s.Require().NoError(err)
	s.Require().False(result.Requeue)
}

// TestSuite runs the GatewayClass controller test suite.
func TestGatewayClassControllerSuite(t *testing.T) {
	suite.Run(t, new(GatewayClassControllerTestSuite))
}
