package testutil

import (
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Mock helpers
var MockAnything = mock.Anything

// Test constants for consistent test data
const (
	TestNamespace     = "test-namespace"
	TestGatewayName   = "test-gateway"
	TestHTTPRouteName = "test-httproute"
	TestGRPCRouteName = "test-grpcroute"
	TestServiceName   = "test-service"
	TestGatewayClass  = "pangolin"
	TestHostname      = "test.example.com"
	TestSiteID        = "12345"
)

// NewTestGateway creates a Gateway resource for testing.
func NewTestGateway(name, namespace string) *gatewayv1.Gateway {
	gatewayClassName := gatewayv1.ObjectName(TestGatewayClass)

	return &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{},
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayClassName,
			Listeners: []gatewayv1.Listener{
				{
					Name:     "http",
					Protocol: gatewayv1.HTTPProtocolType,
					Port:     80,
				},
			},
		},
	}
}

// NewTestHTTPRoute creates an HTTPRoute resource for testing.
func NewTestHTTPRoute(name, namespace, gatewayName string, hostname string) *gatewayv1.HTTPRoute {
	gatewayNamespace := gatewayv1.Namespace(namespace)
	kind := gatewayv1.Kind("Gateway")

	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Name:      gatewayv1.ObjectName(gatewayName),
						Namespace: &gatewayNamespace,
						Kind:      &kind,
					},
				},
			},
			Hostnames: []gatewayv1.Hostname{
				gatewayv1.Hostname(hostname),
			},
			Rules: []gatewayv1.HTTPRouteRule{
				{
					BackendRefs: []gatewayv1.HTTPBackendRef{
						{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name: gatewayv1.ObjectName(TestServiceName),
									Port: ptrTo(gatewayv1.PortNumber(80)),
								},
							},
						},
					},
				},
			},
		},
	}
}

// NewTestGRPCRoute creates a GRPCRoute resource for testing.
func NewTestGRPCRoute(name, namespace, gatewayName string, hostname string) *gatewayv1.GRPCRoute {
	gatewayNamespace := gatewayv1.Namespace(namespace)
	kind := gatewayv1.Kind("Gateway")

	return &gatewayv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				"gateway.pangolin.net/protocol": "tcp",
			},
		},
		Spec: gatewayv1.GRPCRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Name:      gatewayv1.ObjectName(gatewayName),
						Namespace: &gatewayNamespace,
						Kind:      &kind,
					},
				},
			},
			Hostnames: []gatewayv1.Hostname{
				gatewayv1.Hostname(hostname),
			},
			Rules: []gatewayv1.GRPCRouteRule{
				{
					BackendRefs: []gatewayv1.GRPCBackendRef{
						{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name: gatewayv1.ObjectName(TestServiceName),
									Port: ptrTo(gatewayv1.PortNumber(9090)),
								},
							},
						},
					},
				},
			},
		},
	}
}

// NewTestService creates a Service resource for testing.
func NewTestService(name, namespace string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: "10.96.0.100", // Fake ClusterIP for testing
			Ports: []corev1.ServicePort{
				{
					Name:     "http",
					Port:     80,
					Protocol: corev1.ProtocolTCP,
				},
			},
			Selector: map[string]string{
				"app": "test",
			},
		},
	}
}

// NewTestNamespace creates a Namespace for testing.
func NewTestNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

// NewTestSecret creates a Secret for testing.
func NewTestSecret(name, namespace string, data map[string]string) *corev1.Secret {
	stringData := make(map[string]string)
	for k, v := range data {
		stringData[k] = v
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		StringData: stringData,
	}
}

// ptrTo returns a pointer to the given value.
func ptrTo[T any](v T) *T {
	return &v
}
