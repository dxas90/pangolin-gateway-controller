package webhook

import (
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupWebhookWithManager registers the validating webhooks with the manager.
func SetupWebhookWithManager(mgr ctrl.Manager, gatewayClassName string) error {
	decoder := admission.NewDecoder(mgr.GetScheme())

	gatewayValidator := &GatewayValidator{GatewayClassName: gatewayClassName}
	gatewayValidator.InjectDecoder(decoder)

	httpRouteValidator := &HTTPRouteValidator{}
	httpRouteValidator.InjectDecoder(decoder)

	hookServer := mgr.GetWebhookServer()

	hookServer.Register("/validate-gateway", &webhook.Admission{Handler: gatewayValidator})
	hookServer.Register("/validate-httproute", &webhook.Admission{Handler: httpRouteValidator})

	return nil
}

// SetupWebhookWithManagerAndScheme is a convenience for tests that need a custom scheme.
func SetupWebhookWithManagerAndScheme(mgr ctrl.Manager, _ *runtime.Scheme, gatewayClassName string) error {
	return SetupWebhookWithManager(mgr, gatewayClassName)
}
