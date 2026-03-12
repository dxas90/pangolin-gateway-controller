package webhook

import (
	"context"
	"fmt"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	// DisableSSOAnnotation is the annotation key to disable Pangolin SSO.
	DisableSSOAnnotation = "gateway.pangolin.net/disable-sso"
)

// HTTPRouteValidator validates HTTPRoute resources.
type HTTPRouteValidator struct {
	decoder admission.Decoder
}

// Handle validates an HTTPRoute admission request.
func (v *HTTPRouteValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	route := &gatewayv1.HTTPRoute{}
	if err := v.decoder.Decode(req, route); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("failed to decode HTTPRoute: %w", err))
	}

	if len(route.Spec.Hostnames) == 0 {
		return admission.Denied("httproute must have at least one hostname specified")
	}

	for _, ref := range route.Spec.ParentRefs {
		if ref.Name == "" {
			return admission.Denied("httproute parentRef name must not be empty")
		}
	}

	if val, ok := route.Annotations[DisableSSOAnnotation]; ok {
		if val != "true" && val != "false" {
			return admission.Denied(fmt.Sprintf("annotation %s must be \"true\" or \"false\", got %q", DisableSSOAnnotation, val))
		}
	}

	return admission.Allowed("httproute is valid")
}

// InjectDecoder injects the admission decoder.
func (v *HTTPRouteValidator) InjectDecoder(d admission.Decoder) {
	v.decoder = d
}
