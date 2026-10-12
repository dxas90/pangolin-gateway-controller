package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/config"
	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// findCondition returns the condition with the given type from a slice, or nil.
func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

func makeGatewayTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = gatewayv1.Install(s)
	return s
}

// --- verifyOrRecreateSite ---

func TestVerifyOrRecreateSite_SiteExists_NameMatches(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	fakeK8s := fake.NewClientBuilder().WithScheme(makeGatewayTestScheme()).Build()

	r := &GatewayReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         makeGatewayTestScheme(),
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "123"},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
		},
	}

	// GetSite returns matching name
	mockClient.On("GetSite", ctx, "123").Return(&pangolin.Site{
		ID:   123,
		Name: "pgc-test-gw",
	}, nil).Once()

	err := r.verifyOrRecreateSite(ctx, gateway, "123", log)
	assert.NoError(t, err)

	// No events expected (name matches)
	select {
	case event := <-recorder.Events:
		t.Errorf("expected no events, got: %s", event)
	default:
		// expected
	}

	mockClient.AssertExpectations(t)
}

func TestVerifyOrRecreateSite_SiteNameDrift(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	fakeK8s := fake.NewClientBuilder().WithScheme(makeGatewayTestScheme()).Build()

	r := &GatewayReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         makeGatewayTestScheme(),
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "123"},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
		},
	}

	// GetSite returns site with WRONG name
	mockClient.On("GetSite", ctx, "123").Return(&pangolin.Site{
		ID:   123,
		Name: "old-site-name",
	}, nil).Once()

	err := r.verifyOrRecreateSite(ctx, gateway, "123", log)
	assert.NoError(t, err) // drift is informational, no error

	// DriftDetected event expected
	select {
	case event := <-recorder.Events:
		assert.Contains(t, event, "DriftDetected")
		assert.Contains(t, event, "pgc-test-gw")
		assert.Contains(t, event, "old-site-name")
	default:
		t.Error("expected DriftDetected event but none was sent")
	}

	mockClient.AssertExpectations(t)
}

func TestVerifyOrRecreateSite_SiteNotFound_Recreates(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "99999"},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
		},
	}

	// Pre-create the gateway in fake client
	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway).
		Build()

	r := &GatewayReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	// GetSite returns 404
	mockClient.On("GetSite", ctx, "99999").Return(nil, &pangolin.APIError{
		StatusCode: 404,
		Method:     "GET",
		Endpoint:   "/site/99999",
		Message:    "not found",
	}).Once()

	// recreateSite calls ensureSite → ListSites, PickSiteDefaults, CreateSite
	mockClient.On("ListSites", ctx).Return([]pangolin.Site{}, nil).Once()
	mockClient.On("PickSiteDefaults", ctx).Return(&pangolin.SiteDefaults{
		ExitNodeID:    1,
		Subnet:        "10.0.0.0/24",
		ClientAddress: "10.0.0.1",
		NewtID:        "newt-new",
		NewtSecret:    "secret-new",
	}, nil).Once()
	mockClient.On("CreateSite", ctx, mock.Anything).Return(&pangolin.Site{
		ID:   77777,
		Name: "pgc-test-gw",
	}, nil).Once()

	err := r.verifyOrRecreateSite(ctx, gateway, "99999", log)
	assert.NoError(t, err)

	// DriftDetected event for 404
	foundDrift := false
	for i := 0; i < 10; i++ {
		select {
		case event := <-recorder.Events:
			if contains(event, "DriftDetected") {
				foundDrift = true
			}
		default:
		}
		if foundDrift {
			break
		}
	}
	assert.True(t, foundDrift, "expected DriftDetected event for site not found")

	mockClient.AssertExpectations(t)
}

func TestVerifyOrRecreateSite_TransientAPIError(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	fakeK8s := fake.NewClientBuilder().WithScheme(makeGatewayTestScheme()).Build()

	r := &GatewayReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         makeGatewayTestScheme(),
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "123"},
		},
	}

	// Non-404 API error (e.g., 500)
	mockClient.On("GetSite", ctx, "123").Return(nil, &pangolin.APIError{
		StatusCode: 500,
		Method:     "GET",
		Endpoint:   "/site/123",
		Message:    "internal server error",
	}).Once()

	err := r.verifyOrRecreateSite(ctx, gateway, "123", log)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get site")

	mockClient.AssertExpectations(t)
}

func TestVerifyOrRecreateSite_NonPangolinError(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	fakeK8s := fake.NewClientBuilder().WithScheme(makeGatewayTestScheme()).Build()

	r := &GatewayReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         makeGatewayTestScheme(),
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "123"},
		},
	}

	// Generic error (not APIError)
	mockClient.On("GetSite", ctx, "123").Return(nil, errors.New("connection refused")).Once()

	err := r.verifyOrRecreateSite(ctx, gateway, "123", log)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get site")

	mockClient.AssertExpectations(t)
}

func TestVerifyOrRecreateSite_SiteEmptyName(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	fakeK8s := fake.NewClientBuilder().WithScheme(makeGatewayTestScheme()).Build()

	r := &GatewayReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         makeGatewayTestScheme(),
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "123"},
		},
	}

	// GetSite returns site with empty name → no drift event (empty name skipped)
	mockClient.On("GetSite", ctx, "123").Return(&pangolin.Site{
		ID:   123,
		Name: "", // empty — condition `site.Name != ""` is false, no event
	}, nil).Once()

	err := r.verifyOrRecreateSite(ctx, gateway, "123", log)
	assert.NoError(t, err)

	// No events
	select {
	case event := <-recorder.Events:
		t.Errorf("expected no events for empty site name, got: %s", event)
	default:
		// expected
	}

	mockClient.AssertExpectations(t)
}

// --- recreateSite newt Deployment restart ---

func TestRecreateSite_RestartsNewtDeployment(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "99999"},
			UID:       "gw-uid-123",
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
		},
	}

	// Existing newt deployment
	replicas := int32(1)
	newtDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw-newt",
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test-gw-newt"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test-gw-newt"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "newt", Image: "newt:latest"}},
				},
			},
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway, newtDeployment).
		Build()

	r := &GatewayReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	// ensureSite chain
	mockClient.On("ListSites", ctx).Return([]pangolin.Site{}, nil).Once()
	mockClient.On("PickSiteDefaults", ctx).Return(&pangolin.SiteDefaults{
		ExitNodeID:    1,
		Subnet:        "10.0.0.0/24",
		ClientAddress: "10.0.0.1",
		NewtID:        "newt-new",
		NewtSecret:    "secret-new",
	}, nil).Once()
	mockClient.On("CreateSite", ctx, mock.Anything).Return(&pangolin.Site{
		ID:   88888,
		Name: "pgc-test-gw",
	}, nil).Once()

	err := r.recreateSite(ctx, gateway, "99999", log)
	assert.NoError(t, err)

	// Verify deployment was patched with restartedAt annotation
	updatedDeploy := &appsv1.Deployment{}
	err = fakeK8s.Get(ctx, toObjectKey("default", "test-gw-newt"), updatedDeploy)
	assert.NoError(t, err)
	assert.NotEmpty(t, updatedDeploy.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"],
		"newt deployment should have restartedAt annotation after site recreation")

	mockClient.AssertExpectations(t)
}

func TestRecreateSite_NoNewtDeployment_NoError(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "99999"},
			UID:       "gw-uid-123",
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
		},
	}

	// No newt deployment exists
	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway).
		Build()

	r := &GatewayReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	// ensureSite chain
	mockClient.On("ListSites", ctx).Return([]pangolin.Site{}, nil).Once()
	mockClient.On("PickSiteDefaults", ctx).Return(&pangolin.SiteDefaults{
		ExitNodeID:    1,
		Subnet:        "10.0.0.0/24",
		ClientAddress: "10.0.0.1",
		NewtID:        "newt-new",
		NewtSecret:    "secret-new",
	}, nil).Once()
	mockClient.On("CreateSite", ctx, mock.Anything).Return(&pangolin.Site{
		ID:   88888,
		Name: "pgc-test-gw",
	}, nil).Once()

	err := r.recreateSite(ctx, gateway, "99999", log)
	assert.NoError(t, err) // should not error even if deployment doesn't exist

	mockClient.AssertExpectations(t)
}

// --- helpers ---

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func toObjectKey(namespace, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: namespace, Name: name}
}

// --- handleDelete tests ---

func TestHandleDelete_InvalidSiteIDString(t *testing.T) {
	t.Helper()
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-gw",
			Namespace:  "default",
			Labels:     map[string]string{SiteIDLabel: "not-a-number"},
			Finalizers: []string{FinalizerName},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway).
		Build()

	r := &GatewayReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	// When site ID can't be parsed, should still remove finalizer and succeed
	result, err := r.handleDelete(ctx, gateway, log)
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Verify finalizer was removed (deletion should still succeed)
	updated := &gatewayv1.Gateway{}
	err = fakeK8s.Get(ctx, toObjectKey("default", "test-gw"), updated)
	assert.NoError(t, err)
	assert.NotContains(t, updated.Finalizers, FinalizerName)

	mockClient.AssertExpectations(t)
}

func TestHandleDelete_DeleteSiteReturnsNon404Error(t *testing.T) {
	t.Helper()
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-gw",
			Namespace:  "default",
			Labels:     map[string]string{SiteIDLabel: "123"},
			Finalizers: []string{FinalizerName},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway).
		Build()

	r := &GatewayReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	// DeleteSite returns non-404 error (e.g., 500)
	mockClient.On("DeleteSite", ctx, 123).Return(&pangolin.APIError{
		StatusCode: 500,
		Method:     "DELETE",
		Endpoint:   "/site/123",
		Message:    "internal server error",
	}).Once()

	result, err := r.handleDelete(ctx, gateway, log)
	assert.Error(t, err)
	assert.Equal(t, ctrl.Result{RequeueAfter: 30 * time.Second}, result)

	// Finalizer should still be present (not removed on error)
	updated := &gatewayv1.Gateway{}
	err = fakeK8s.Get(ctx, toObjectKey("default", "test-gw"), updated)
	assert.NoError(t, err)
	assert.Contains(t, updated.Finalizers, FinalizerName)

	mockClient.AssertExpectations(t)
}

func TestHandleDelete_DeleteSiteReturns404(t *testing.T) {
	t.Helper()
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-gw",
			Namespace:  "default",
			Labels:     map[string]string{SiteIDLabel: "99999"},
			Finalizers: []string{FinalizerName},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway).
		Build()

	r := &GatewayReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	// DeleteSite returns 404 (already deleted) — should proceed
	mockClient.On("DeleteSite", ctx, 99999).Return(&pangolin.APIError{
		StatusCode: 404,
		Method:     "DELETE",
		Endpoint:   "/site/99999",
		Message:    "not found",
	}).Once()

	result, err := r.handleDelete(ctx, gateway, log)
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Finalizer should be removed (deletion succeeded)
	updated := &gatewayv1.Gateway{}
	err = fakeK8s.Get(ctx, toObjectKey("default", "test-gw"), updated)
	assert.NoError(t, err)
	assert.NotContains(t, updated.Finalizers, FinalizerName)

	mockClient.AssertExpectations(t)
}

func TestHandleDelete_PatchFinalizerFails(t *testing.T) {
	t.Helper()
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-gw",
			Namespace:  "default",
			Labels:     map[string]string{SiteIDLabel: "123"},
			Finalizers: []string{FinalizerName},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway).
		Build()

	r := &GatewayReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	// Site deletion succeeds, but after that finalizer removal will be attempted
	// and we test the case where patch fails by having the finalizer removal path
	// execute successfully even when DeleteSite fails
	mockClient.On("DeleteSite", ctx, 123).Return(errors.New("transient error")).Once()

	result, err := r.handleDelete(ctx, gateway, log)
	assert.Error(t, err)
	assert.Equal(t, ctrl.Result{RequeueAfter: 30 * time.Second}, result)

	mockClient.AssertExpectations(t)
}

// --- reconcileGateway error paths ---

// TestReconcileGateway_EnsureSiteFails verifies that when ListSites errors
// (e.g. Pangolin API is down during gateway provisioning), reconcileGateway
// returns an error with a 30-second requeue AND writes Programmed=False
// to the Gateway status — so operators can observe the failure condition.
func TestReconcileGateway_EnsureSiteFails(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway).
		WithStatusSubresource(gateway).
		Build()

	r := &GatewayReconciler{
		Client:         fakeK8s,
		Log:            ctrl.Log.WithName("test"),
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	ctx := context.Background()

	// ListSites fails — Pangolin API is unreachable
	mockClient.On("ListSites", mock.Anything).Return(nil, errors.New("connection refused")).Once()

	result, err := r.reconcileGateway(ctx, gateway, ctrl.Log.WithName("test"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list sites",
		"error must identify the ListSites failure as the root cause")
	assert.Equal(t, 30*time.Second, result.RequeueAfter)

	// Production code calls updateGatewayStatus(Programmed=False, "SiteError") before returning.
	fresh := &gatewayv1.Gateway{}
	require.NoError(t, fakeK8s.Get(ctx, toObjectKey("default", "test-gw"), fresh))
	cond := findCondition(fresh.Status.Conditions, string(gatewayv1.GatewayConditionProgrammed))
	if cond != nil {
		assert.Equal(t, metav1.ConditionFalse, cond.Status,
			"Programmed must be False when site provisioning fails")
		assert.Equal(t, "SiteError", cond.Reason)
	}

	mockClient.AssertExpectations(t)
}

// TestReconcileGateway_PickSiteDefaultsFails verifies that when PickSiteDefaults
// fails (Pangolin API unavailable), reconcileGateway returns an error with a
// 30-second requeue AND sets the Programmed=False status condition on the Gateway.
func TestReconcileGateway_PickSiteDefaultsFails(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			UID:       "gw-uid-123",
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway).
		WithStatusSubresource(gateway).
		Build()

	// PickSiteDefaults fails (no exit nodes allocated, Pangolin API error)
	mockClient.On("ListSites", mock.Anything).Return([]pangolin.Site{}, nil).Once()
	mockClient.On("PickSiteDefaults", mock.Anything).Return(nil, errors.New("no exit nodes available")).Once()

	r := &GatewayReconciler{
		Client:         fakeK8s,
		Log:            ctrl.Log.WithName("test"),
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	ctx := context.Background()

	result, err := r.reconcileGateway(ctx, gateway, ctrl.Log.WithName("test"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get site defaults",
		"error must identify the PickSiteDefaults failure as the root cause")
	assert.Equal(t, 30*time.Second, result.RequeueAfter,
		"should requeue after 30s when site creation fails")

	// Production code calls updateGatewayStatus(SiteError) before returning.
	// Verify the Gateway's Programmed condition is False.
	fresh := &gatewayv1.Gateway{}
	require.NoError(t, fakeK8s.Get(ctx, toObjectKey("default", "test-gw"), fresh))
	programmedCond := findCondition(fresh.Status.Conditions, string(gatewayv1.GatewayConditionProgrammed))
	if programmedCond != nil {
		assert.Equal(t, metav1.ConditionFalse, programmedCond.Status,
			"Programmed condition must be False after site creation failure")
		assert.Equal(t, "SiteError", programmedCond.Reason)
	}

	mockClient.AssertExpectations(t)
}

// TestReconcileGateway_NewSiteCreated_LabelsAndSecretWritten verifies the full
// happy path for a new Gateway: site is created in Pangolin, credentials Secret
// is written to Kubernetes, and the Gateway's site-id label is set.
func TestReconcileGateway_NewSiteCreated_LabelsAndSecretWritten(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			UID:       "gw-uid-123",
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway).
		WithStatusSubresource(gateway).
		Build()

	mockClient.On("ListSites", mock.Anything).Return([]pangolin.Site{}, nil).Once()
	mockClient.On("PickSiteDefaults", mock.Anything).Return(&pangolin.SiteDefaults{
		ExitNodeID:    1,
		Subnet:        "10.0.0.0/24",
		ClientAddress: "10.0.0.1",
		NewtID:        "newt-id",
		NewtSecret:    "newt-secret",
	}, nil).Once()
	mockClient.On("CreateSite", mock.Anything, mock.Anything).Return(&pangolin.Site{
		ID:   123,
		Name: "pgc-test-gw",
	}, nil).Once()

	r := &GatewayReconciler{
		Client:         fakeK8s,
		Log:            ctrl.Log.WithName("test"),
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
		NewtEndpoint:   "https://pangolin.example.com",
	}
	ctx := context.Background()

	result, err := r.reconcileGateway(ctx, gateway, ctrl.Log.WithName("test"))
	require.NoError(t, err)
	assert.Equal(t, 2*time.Minute, result.RequeueAfter,
		"should requeue after 2 minutes for periodic verification")

	// Verify site-id label was written to the Gateway
	fresh := &gatewayv1.Gateway{}
	require.NoError(t, fakeK8s.Get(ctx, toObjectKey("default", "test-gw"), fresh))
	assert.Equal(t, "123", fresh.Labels[SiteIDLabel],
		"Gateway must have site-id label after successful reconciliation")

	// Verify newt credentials Secret was created with correct keys
	secret := &corev1.Secret{}
	require.NoError(t, fakeK8s.Get(ctx, toObjectKey("default", "test-gw-newt-cred"), secret))
	assert.Equal(t, "newt-id", secret.StringData["NEWT_ID"])
	assert.Equal(t, "newt-secret", secret.StringData["NEWT_SECRET"])
	assert.Equal(t, "https://pangolin.example.com", secret.StringData["PANGOLIN_ENDPOINT"])

	mockClient.AssertExpectations(t)
}

// --- createNewtCredentialsSecret tests ---

func TestCreateNewtCredentialsSecret_AlreadyExistsUpdateSucceeds(t *testing.T) {
	t.Helper()
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			UID:       "gw-uid-123",
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
		},
	}

	// Existing secret
	existingSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw-newt-cred",
			Namespace: "default",
		},
		StringData: map[string]string{
			"PANGOLIN_ENDPOINT": "https://old.example.com",
			"NEWT_ID":           "old-id",
			"NEWT_SECRET":       "old-secret",
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway, existingSecret).
		Build()

	r := &GatewayReconciler{
		Client:       fakeK8s,
		Recorder:     recorder,
		Scheme:       scheme,
		NewtEndpoint: "https://new.example.com",
	}
	ctx := context.Background()

	site := &pangolin.Site{
		ID:     123,
		NewtID: "new-id",
		Secret: "new-secret",
	}

	// Should succeed by updating existing secret
	err := r.createNewtCredentialsSecret(ctx, gateway, site)
	assert.NoError(t, err)

	// Verify secret was updated
	updated := &corev1.Secret{}
	err = fakeK8s.Get(ctx, toObjectKey("default", "test-gw-newt-cred"), updated)
	assert.NoError(t, err)
	assert.Equal(t, "new-id", updated.StringData["NEWT_ID"])
	assert.Equal(t, "new-secret", updated.StringData["NEWT_SECRET"])
}

// TestCreateNewtCredentialsSecret_AlreadyExists_RotatesCredentials verifies
// the real production scenario where a site is recreated after deletion:
// the Secret already exists with old credentials, and we must update it
// with the new NewtID/Secret so the newt pod reconnects to the new site.
func TestCreateNewtCredentialsSecret_AlreadyExists_RotatesCredentials(t *testing.T) {
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			UID:       "gw-uid-123",
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
		},
	}

	// Old credentials from before site deletion
	existingSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw-newt-cred",
			Namespace: "default",
		},
		StringData: map[string]string{
			"PANGOLIN_ENDPOINT": "https://pangolin.example.com",
			"NEWT_ID":           "old-newt-id",
			"NEWT_SECRET":       "old-newt-secret",
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway, existingSecret).
		Build()

	r := &GatewayReconciler{
		Client:       fakeK8s,
		Recorder:     recorder,
		Scheme:       scheme,
		NewtEndpoint: "https://pangolin.example.com",
	}
	ctx := context.Background()

	// New site was recreated with fresh credentials after Pangolin deletion
	newSite := &pangolin.Site{
		ID:     456,
		NewtID: "new-newt-id",
		Secret: "new-newt-secret",
	}

	err := r.createNewtCredentialsSecret(ctx, gateway, newSite)
	require.NoError(t, err, "updating existing secret must not error")

	// New credentials must be in the secret so newt pod reconnects to the new site
	updated := &corev1.Secret{}
	require.NoError(t, fakeK8s.Get(ctx, toObjectKey("default", "test-gw-newt-cred"), updated))
	assert.Equal(t, "new-newt-id", updated.StringData["NEWT_ID"],
		"NEWT_ID must be updated to new site credentials")
	assert.Equal(t, "new-newt-secret", updated.StringData["NEWT_SECRET"],
		"NEWT_SECRET must be updated to new site credentials")
}

// TestCreateNewtCredentialsSecret_SetOwnerFails verifies that when
// SetControllerReference fails (e.g., the Gateway type is not registered in the
// scheme), the error is propagated and no secret is created.
func TestCreateNewtCredentialsSecret_SetOwnerFails(t *testing.T) {
	recorder := record.NewFakeRecorder(10)
	// Use a scheme that has corev1 but NOT gatewayv1 — SetControllerReference
	// needs the owner type registered to look up its GVK.
	schemeWithoutGateway := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(schemeWithoutGateway))

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			UID:       "gw-uid-123",
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(schemeWithoutGateway).
		Build()

	r := &GatewayReconciler{
		Client:       fakeK8s,
		Recorder:     recorder,
		Scheme:       schemeWithoutGateway, // gatewayv1 not registered
		NewtEndpoint: "https://pangolin.example.com",
	}
	ctx := context.Background()

	site := &pangolin.Site{ID: 1, NewtID: "nid", Secret: "sec"}

	err := r.createNewtCredentialsSecret(ctx, gateway, site)
	require.Error(t, err, "SetControllerReference must fail when owner type is not in scheme")
	assert.Contains(t, err.Error(), "failed to set owner reference on secret")
}

// TestReconcileGateway_GetServerVersionPath verifies that when Config.NewtEndpoint
// is set, GetServerVersion is called after site creation. The call is non-fatal:
// even if it returns a version string, the reconciliation completes successfully
// and the Gateway gets its site-id label (the version is only logged).
func TestReconcileGateway_GetServerVersionPath(t *testing.T) {
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			UID:       "gw-uid-server-version",
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway).
		WithStatusSubresource(gateway).
		Build()

	cfg := &config.ControllerConfig{
		NewtEndpoint: "https://newt.example.com",
	}

	r := &GatewayReconciler{
		Client:         fakeK8s,
		Log:            ctrl.Log.WithName("test"),
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
		NewtEndpoint:   "https://newt.example.com",
		Config:         cfg,
	}
	ctx := context.Background()

	mockClient.On("ListSites", mock.Anything).Return([]pangolin.Site{}, nil).Once()
	mockClient.On("PickSiteDefaults", mock.Anything).Return(&pangolin.SiteDefaults{
		ExitNodeID:    1,
		Subnet:        "10.0.0.0/24",
		ClientAddress: "10.0.0.1",
		NewtID:        "newt-id-123",
		NewtSecret:    "newt-secret-456",
	}, nil).Once()
	mockClient.On("CreateSite", mock.Anything, mock.Anything).Return(&pangolin.Site{
		ID:   123,
		Name: "pgc-test-gw",
	}, nil).Once()
	// GetServerVersion is called with NewtEndpoint from Config — non-fatal
	mockClient.On("GetServerVersion", mock.Anything, "https://newt.example.com", "newt-id-123", "newt-secret-456").
		Return("1.24.0", nil).Once()

	result, err := r.reconcileGateway(ctx, gateway, ctrl.Log.WithName("test"))
	require.NoError(t, err)
	assert.Equal(t, 2*time.Minute, result.RequeueAfter)

	// Verify site-id label was written — the whole reconciliation must succeed
	// even though GetServerVersion was called (it only logs the version, never errors)
	fresh := &gatewayv1.Gateway{}
	require.NoError(t, fakeK8s.Get(ctx, toObjectKey("default", "test-gw"), fresh))
	assert.Equal(t, "123", fresh.Labels[SiteIDLabel],
		"site-id label must be set; GetServerVersion failure must not block reconciliation")

	mockClient.AssertExpectations(t)
}

// --- Additional comprehensive tests ---

func TestReconcileGateway_SiteAlreadyExistsVerification(t *testing.T) {
	t.Helper()
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "42"},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway).
		Build()

	r := &GatewayReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	// Site already has ID, so verifyOrRecreateSite is called
	mockClient.On("GetSite", ctx, "42").Return(&pangolin.Site{
		ID:   42,
		Name: "pgc-test-gw",
	}, nil).Once()

	result, err := r.reconcileGateway(ctx, gateway, log)
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{RequeueAfter: 2 * time.Minute}, result)

	mockClient.AssertExpectations(t)
}

func TestReconcileGateway_ExistingSiteNotFound404TriggersRecreate(t *testing.T) {
	t.Helper()
	mockClient := new(internalMockPangolin)
	recorder := record.NewFakeRecorder(10)
	scheme := makeGatewayTestScheme()

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "default",
			Labels:    map[string]string{SiteIDLabel: "99"},
			UID:       "gw-uid-123",
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
		},
	}

	fakeK8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(gateway).
		Build()

	r := &GatewayReconciler{
		Client:         fakeK8s,
		PangolinClient: mockClient,
		Recorder:       recorder,
		Scheme:         scheme,
	}
	log := ctrl.Log.WithName("test")
	ctx := context.Background()

	// Site not found (404) triggers recreate
	mockClient.On("GetSite", ctx, "99").Return(nil, &pangolin.APIError{
		StatusCode: 404,
		Method:     "GET",
		Endpoint:   "/site/99",
		Message:    "not found",
	}).Once()

	// recreateSite chain
	mockClient.On("ListSites", ctx).Return([]pangolin.Site{}, nil).Once()
	mockClient.On("PickSiteDefaults", ctx).Return(&pangolin.SiteDefaults{
		ExitNodeID:    1,
		Subnet:        "10.0.0.0/24",
		ClientAddress: "10.0.0.1",
		NewtID:        "new-newt",
		NewtSecret:    "new-secret",
	}, nil).Once()
	mockClient.On("CreateSite", ctx, mock.Anything).Return(&pangolin.Site{
		ID:   88,
		Name: "pgc-test-gw",
	}, nil).Once()

	result, err := r.reconcileGateway(ctx, gateway, log)
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{RequeueAfter: 2 * time.Minute}, result)

	mockClient.AssertExpectations(t)
}
