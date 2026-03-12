package webhook

import (
	"context"
	"fmt"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// GatewayValidator validates Gateway resources.
type GatewayValidator struct {
	GatewayClassName string
	decoder          admission.Decoder
}

// Handle validates a Gateway admission request.
func (v *GatewayValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	gateway := &gatewayv1.Gateway{}
	if err := v.decoder.Decode(req, gateway); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("failed to decode Gateway: %w", err))
	}

	// Only validate Gateways for our GatewayClass
	if string(gateway.Spec.GatewayClassName) != v.GatewayClassName {
		return admission.Allowed("gateway is not managed by this controller")
	}

	if len(gateway.Spec.Listeners) == 0 {
		return admission.Denied("gateway must have at least one listener configured")
	}

	return admission.Allowed("gateway is valid")
}

// InjectDecoder injects the admission decoder.
func (v *GatewayValidator) InjectDecoder(d admission.Decoder) {
	v.decoder = d
}
