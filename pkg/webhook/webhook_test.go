package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// buildWebhookScheme returns a scheme with the Gateway API types registered so
// the admission decoder can deserialize Gateway and HTTPRoute objects.
func buildWebhookScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, gatewayv1.Install(s))
	return s
}

// newDecoder builds a real admission decoder backed by the Gateway API scheme.
func newDecoder(t *testing.T) admission.Decoder {
	t.Helper()
	return admission.NewDecoder(buildWebhookScheme(t))
}

// admissionRequestFor marshals obj into a raw admission request body. The GVK
// must be set explicitly because the API server strips it from embedded objects
// and the decoder needs it to pick the right type.
func admissionRequestFor(t *testing.T, obj runtime.Object, gvk metav1.GroupVersionKind) admission.Request {
	t.Helper()
	raw, err := json.Marshal(obj)
	require.NoError(t, err)
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Kind:   gvk,
			Object: runtime.RawExtension{Raw: raw},
		},
	}
}

var (
	gatewayGVK   = metav1.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway"}
	httpRouteGVK = metav1.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"}
)

// ---------------------------------------------------------------------------
// GatewayValidator.Handle
// ---------------------------------------------------------------------------

// TestGatewayValidator_ValidGatewayAllowed verifies a Gateway for our class with
// at least one listener is admitted.
func TestGatewayValidator_ValidGatewayAllowed(t *testing.T) {
	v := &GatewayValidator{GatewayClassName: "pangolin"}
	v.InjectDecoder(newDecoder(t))

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "default"},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
			Listeners: []gatewayv1.Listener{
				{Name: "http", Protocol: gatewayv1.HTTPProtocolType, Port: 80},
			},
		},
	}

	resp := v.Handle(context.Background(), admissionRequestFor(t, gateway, gatewayGVK))
	assert.True(t, resp.Allowed, "valid gateway must be admitted")
}

// TestGatewayValidator_WrongClassAllowedUnvalidated verifies a Gateway managed by
// a different controller is admitted without validation (we must not block
// resources we do not own, even if they would be invalid under our rules).
func TestGatewayValidator_WrongClassAllowedUnvalidated(t *testing.T) {
	v := &GatewayValidator{GatewayClassName: "pangolin"}
	v.InjectDecoder(newDecoder(t))

	// No listeners — would be denied if it were ours, but it is not our class.
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "default"},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "some-other-controller",
			Listeners:        []gatewayv1.Listener{},
		},
	}

	resp := v.Handle(context.Background(), admissionRequestFor(t, gateway, gatewayGVK))
	assert.True(t, resp.Allowed, "gateway for another class must be allowed through unvalidated")
	assert.Contains(t, resp.Result.Message, "not managed by this controller")
}

// TestGatewayValidator_NoListenersDenied verifies a Gateway for our class with no
// listeners is rejected at admission time (the controller cannot provision a
// site without at least one listener).
func TestGatewayValidator_NoListenersDenied(t *testing.T) {
	v := &GatewayValidator{GatewayClassName: "pangolin"}
	v.InjectDecoder(newDecoder(t))

	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "default"},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "pangolin",
			Listeners:        []gatewayv1.Listener{},
		},
	}

	resp := v.Handle(context.Background(), admissionRequestFor(t, gateway, gatewayGVK))
	assert.False(t, resp.Allowed, "gateway with no listeners must be denied")
	assert.Contains(t, resp.Result.Message, "at least one listener")
}

// TestGatewayValidator_DecodeErrorBadRequest verifies that a malformed admission
// payload is rejected with HTTP 400 rather than being silently allowed.
func TestGatewayValidator_DecodeErrorBadRequest(t *testing.T) {
	v := &GatewayValidator{GatewayClassName: "pangolin"}
	v.InjectDecoder(newDecoder(t))

	badReq := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Kind:   gatewayGVK,
			Object: runtime.RawExtension{Raw: []byte(`{not valid json`)},
		},
	}

	resp := v.Handle(context.Background(), badReq)
	assert.False(t, resp.Allowed, "undecodable gateway must not be admitted")
	require.NotNil(t, resp.Result)
	assert.Equal(t, int32(http.StatusBadRequest), resp.Result.Code)
}

// TestGatewayValidator_EmptyBodyBadRequest verifies an empty request body is
// rejected (the decoder errors on zero raw bytes).
func TestGatewayValidator_EmptyBodyBadRequest(t *testing.T) {
	v := &GatewayValidator{GatewayClassName: "pangolin"}
	v.InjectDecoder(newDecoder(t))

	emptyReq := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Kind:   gatewayGVK,
			Object: runtime.RawExtension{Raw: []byte{}},
		},
	}

	resp := v.Handle(context.Background(), emptyReq)
	assert.False(t, resp.Allowed)
	require.NotNil(t, resp.Result)
	assert.Equal(t, int32(http.StatusBadRequest), resp.Result.Code)
}

// ---------------------------------------------------------------------------
// HTTPRouteValidator.Handle
// ---------------------------------------------------------------------------

func newHTTPRoute(hostnames []gatewayv1.Hostname, parentNames []string, annotations map[string]string) *gatewayv1.HTTPRoute {
	var parents []gatewayv1.ParentReference
	for _, n := range parentNames {
		parents = append(parents, gatewayv1.ParentReference{Name: gatewayv1.ObjectName(n)})
	}
	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "route", Namespace: "default", Annotations: annotations},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: parents},
			Hostnames:       hostnames,
		},
	}
}

// TestHTTPRouteValidator_ValidRouteAllowed verifies a route with a hostname and a
// named parentRef is admitted.
func TestHTTPRouteValidator_ValidRouteAllowed(t *testing.T) {
	v := &HTTPRouteValidator{}
	v.InjectDecoder(newDecoder(t))

	route := newHTTPRoute([]gatewayv1.Hostname{"app.example.com"}, []string{"my-gateway"}, nil)
	resp := v.Handle(context.Background(), admissionRequestFor(t, route, httpRouteGVK))
	assert.True(t, resp.Allowed, "valid httproute must be admitted")
}

// TestHTTPRouteValidator_NoHostnamesDenied verifies a route with no hostnames is
// rejected — the controller cannot create a Pangolin resource without one.
func TestHTTPRouteValidator_NoHostnamesDenied(t *testing.T) {
	v := &HTTPRouteValidator{}
	v.InjectDecoder(newDecoder(t))

	route := newHTTPRoute(nil, []string{"my-gateway"}, nil)
	resp := v.Handle(context.Background(), admissionRequestFor(t, route, httpRouteGVK))
	assert.False(t, resp.Allowed)
	assert.Contains(t, resp.Result.Message, "at least one hostname")
}

// TestHTTPRouteValidator_EmptyParentRefNameDenied verifies a parentRef with an
// empty name is rejected (it cannot be resolved to a Gateway).
func TestHTTPRouteValidator_EmptyParentRefNameDenied(t *testing.T) {
	v := &HTTPRouteValidator{}
	v.InjectDecoder(newDecoder(t))

	route := newHTTPRoute([]gatewayv1.Hostname{"app.example.com"}, []string{""}, nil)
	resp := v.Handle(context.Background(), admissionRequestFor(t, route, httpRouteGVK))
	assert.False(t, resp.Allowed)
	assert.Contains(t, resp.Result.Message, "parentRef name must not be empty")
}

// TestHTTPRouteValidator_DisableSSOTrueAllowed verifies the disable-sso annotation
// with the valid value "true" is admitted.
func TestHTTPRouteValidator_DisableSSOTrueAllowed(t *testing.T) {
	v := &HTTPRouteValidator{}
	v.InjectDecoder(newDecoder(t))

	route := newHTTPRoute([]gatewayv1.Hostname{"app.example.com"}, []string{"my-gateway"},
		map[string]string{DisableSSOAnnotation: "true"})
	resp := v.Handle(context.Background(), admissionRequestFor(t, route, httpRouteGVK))
	assert.True(t, resp.Allowed, "disable-sso=true is a valid annotation value")
}

// TestHTTPRouteValidator_DisableSSOFalseAllowed verifies the disable-sso annotation
// with the valid value "false" is admitted.
func TestHTTPRouteValidator_DisableSSOFalseAllowed(t *testing.T) {
	v := &HTTPRouteValidator{}
	v.InjectDecoder(newDecoder(t))

	route := newHTTPRoute([]gatewayv1.Hostname{"app.example.com"}, []string{"my-gateway"},
		map[string]string{DisableSSOAnnotation: "false"})
	resp := v.Handle(context.Background(), admissionRequestFor(t, route, httpRouteGVK))
	assert.True(t, resp.Allowed, "disable-sso=false is a valid annotation value")
}

// TestHTTPRouteValidator_DisableSSOInvalidValueDenied verifies a disable-sso
// annotation with a value other than true/false is rejected, so misconfiguration
// is caught at admission rather than silently ignored at reconcile time.
func TestHTTPRouteValidator_DisableSSOInvalidValueDenied(t *testing.T) {
	v := &HTTPRouteValidator{}
	v.InjectDecoder(newDecoder(t))

	route := newHTTPRoute([]gatewayv1.Hostname{"app.example.com"}, []string{"my-gateway"},
		map[string]string{DisableSSOAnnotation: "yes"})
	resp := v.Handle(context.Background(), admissionRequestFor(t, route, httpRouteGVK))
	assert.False(t, resp.Allowed)
	assert.Contains(t, resp.Result.Message, "must be")
}

// TestHTTPRouteValidator_NoAnnotationAllowed verifies a route without the
// disable-sso annotation is admitted (annotation is optional).
func TestHTTPRouteValidator_NoAnnotationAllowed(t *testing.T) {
	v := &HTTPRouteValidator{}
	v.InjectDecoder(newDecoder(t))

	route := newHTTPRoute([]gatewayv1.Hostname{"app.example.com"}, []string{"my-gateway"}, nil)
	resp := v.Handle(context.Background(), admissionRequestFor(t, route, httpRouteGVK))
	assert.True(t, resp.Allowed)
}

// TestHTTPRouteValidator_MultipleParentRefsOneEmptyDenied verifies that when one
// of several parentRefs has an empty name, the whole route is denied.
func TestHTTPRouteValidator_MultipleParentRefsOneEmptyDenied(t *testing.T) {
	v := &HTTPRouteValidator{}
	v.InjectDecoder(newDecoder(t))

	route := newHTTPRoute([]gatewayv1.Hostname{"app.example.com"}, []string{"good-gateway", ""}, nil)
	resp := v.Handle(context.Background(), admissionRequestFor(t, route, httpRouteGVK))
	assert.False(t, resp.Allowed, "a single empty parentRef name must deny the whole route")
	assert.Contains(t, resp.Result.Message, "parentRef name must not be empty")
}

// TestHTTPRouteValidator_DecodeErrorBadRequest verifies malformed HTTPRoute
// payloads are rejected with HTTP 400.
func TestHTTPRouteValidator_DecodeErrorBadRequest(t *testing.T) {
	v := &HTTPRouteValidator{}
	v.InjectDecoder(newDecoder(t))

	badReq := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Kind:   httpRouteGVK,
			Object: runtime.RawExtension{Raw: []byte(`{broken`)},
		},
	}
	resp := v.Handle(context.Background(), badReq)
	assert.False(t, resp.Allowed)
	require.NotNil(t, resp.Result)
	assert.Equal(t, int32(http.StatusBadRequest), resp.Result.Code)
}
