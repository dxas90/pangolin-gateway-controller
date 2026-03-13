package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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
	mockClient.On("GetSite", ctx, "99999").Return(nil, &pangolin.PangolinAPIError{
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
			break
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
	mockClient.On("GetSite", ctx, "123").Return(nil, &pangolin.PangolinAPIError{
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

	// Generic error (not PangolinAPIError)
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
