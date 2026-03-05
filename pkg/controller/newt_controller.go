// Package controller implements Kubernetes controllers for the Pangolin Gateway integration.
// The NewtReconciler automatically deploys newt VPN pods for Gateways with Pangolin sites.
package controller

import (
	"context"
	"fmt"

	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	// NewtSecretLabel identifies Secrets containing newt credentials
	NewtSecretLabel = "gateway.pangolin.net/newt-secret"

	// NewtDeploymentLabel identifies newt Deployments
	NewtDeploymentLabel = "gateway.pangolin.net/newt-deployment"

	// NewtServiceLabel identifies newt Services
	NewtServiceLabel = "gateway.pangolin.net/newt-service"

	// NewtImage is the Docker image for the newt VPN client
	NewtImage = "docker.io/fosrl/newt:1.10.0"

	// NewtWireGuardPort is the UDP port for WireGuard VPN traffic
	NewtWireGuardPort = 51820

	// NewtTesterPort is the UDP port for newt health checks
	NewtTesterPort = 51821
)

// NewtReconciler watches Gateway resources and automatically deploys newt VPN
// instances to establish WireGuard tunnels to Pangolin. It creates a Secret,
// Deployment, and Service for each Gateway that has a Pangolin site configured.
// All resources use OwnerReferences for automatic cleanup when the Gateway is deleted.
type NewtReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Log             logr.Logger
	PangolinClient  *pangolin.Client
	PangolinBaseURL string
	NewtEndpoint    string
	NewtImage       string
	Recorder        record.EventRecorder
}

// Reconcile ensures a newt VPN deployment exists for Gateways with Pangolin sites.
// It reads newt credentials from a Secret created by the GatewayReconciler and
// deploys a newt pod that establishes a WireGuard tunnel to the Pangolin exit node.
//
// Returns:
//   - ctrl.Result: Indicates if/when reconciliation should be retried
//   - error: Any error encountered during reconciliation
func (r *NewtReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("gateway", req.NamespacedName)

	// Fetch the Gateway
	var gateway gatewayv1.Gateway
	if err := r.Get(ctx, req.NamespacedName, &gateway); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Skip if not our gateway class
	if gateway.Spec.GatewayClassName != GatewayClassName {
		return ctrl.Result{}, nil
	}

	// Check if gateway has a site ID
	siteID := gateway.Labels[SiteIDLabel]
	if siteID == "" {
		log.Info("Gateway does not have site ID yet, skipping newt deployment")
		return ctrl.Result{}, nil
	}

	// Get newt credentials from Secret
	secretName := fmt.Sprintf("%s-newt-cred", gateway.Name)
	secret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: gateway.Namespace, Name: secretName}, secret); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Newt credentials secret not found yet, skipping newt deployment")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get newt credentials secret")
		return ctrl.Result{}, err
	}

	// Build site object from secret
	site := &pangolin.Site{
		NewtID: string(secret.Data["NEWT_ID"]),
		Secret: string(secret.Data["NEWT_SECRET"]),
	}

	// Ensure newt deployment exists
	if err := r.ensureNewtDeployment(ctx, &gateway, site, log); err != nil {
		log.Error(err, "Failed to ensure newt deployment")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// ensureNewtDeployment creates or updates the complete newt deployment stack
// for a Gateway. This includes:
//   - Secret: Contains PANGOLIN_ENDPOINT, NEWT_ID, and NEWT_SECRET
//   - Deployment: Runs the fosrl/newt:1.10.0 container with WireGuard
//   - Service: Exposes UDP ports 51820 (WireGuard) and 51821 (health check)
//
// All resources are owned by the Gateway and will be deleted when the Gateway is deleted.
func (r *NewtReconciler) ensureNewtDeployment(ctx context.Context, gateway *gatewayv1.Gateway, site *pangolin.Site, log logr.Logger) error {
	// Create secret with newt credentials
	secret := r.buildNewtSecret(gateway, site)
	if err := r.createOrUpdateSecret(ctx, secret); err != nil {
		return fmt.Errorf("failed to create newt secret: %w", err)
	}

	// Create deployment
	deployment := r.buildNewtDeployment(gateway, site)
	if err := r.createOrUpdateDeployment(ctx, deployment); err != nil {
		return fmt.Errorf("failed to create newt deployment: %w", err)
	}

	// Create service
	service := r.buildNewtService(gateway, site)
	if err := r.createOrUpdateService(ctx, service); err != nil {
		return fmt.Errorf("failed to create newt service: %w", err)
	}

	log.Info("Newt deployment ensured", "siteID", site.ID, "deployment", deployment.Name)
	return nil
}

// buildNewtSecret creates a Secret manifest with newt VPN credentials.
// The Secret contains environment variables that the newt container needs
// to authenticate with the Pangolin API and establish the VPN tunnel.
func (r *NewtReconciler) buildNewtSecret(gateway *gatewayv1.Gateway, site *pangolin.Site) *corev1.Secret {
	secretName := fmt.Sprintf("%s-newt-cred", gateway.Name)

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: gateway.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "newt",
				"app.kubernetes.io/instance":   gateway.Name,
				"app.kubernetes.io/managed-by": "pangolin-gateway-controller",
				NewtSecretLabel:                gateway.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: gateway.APIVersion,
					Kind:       gateway.Kind,
					Name:       gateway.Name,
					UID:        gateway.UID,
					Controller: ptr(true),
				},
			},
		},
		StringData: map[string]string{
			"PANGOLIN_ENDPOINT": r.NewtEndpoint,
			"NEWT_ID":           site.NewtID,
			"NEWT_SECRET":       site.Secret,
		},
	}
}

// buildNewtDeployment creates a Deployment manifest for the newt VPN instance.
// The Deployment runs a single replica of the fosrl/newt container with:
//   - NET_ADMIN capability for WireGuard tunnel creation
//   - Environment variables from the newt credentials Secret
//   - Resource requests/limits for production stability
//   - UDP ports for WireGuard (51820) and health checks (51821)
func (r *NewtReconciler) buildNewtDeployment(gateway *gatewayv1.Gateway, site *pangolin.Site) *appsv1.Deployment {
	deploymentName := fmt.Sprintf("%s-newt", gateway.Name)
	secretName := fmt.Sprintf("%s-newt-cred", gateway.Name)
	replicas := int32(1)
	revisionHistoryLimit := int32(3)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: gateway.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "newt",
				"app.kubernetes.io/instance":   gateway.Name,
				"app.kubernetes.io/managed-by": "pangolin-gateway-controller",
				"app.kubernetes.io/component":  "newt",
				NewtDeploymentLabel:            gateway.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: gateway.APIVersion,
					Kind:       gateway.Kind,
					Name:       gateway.Name,
					UID:        gateway.UID,
					Controller: ptr(true),
				},
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas:             &replicas,
			RevisionHistoryLimit: &revisionHistoryLimit,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/instance": gateway.Name,
					"newt.instance":              "main",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/name":      "newt",
						"app.kubernetes.io/instance":  gateway.Name,
						"app.kubernetes.io/component": "newt",
						"newt.instance":               "main",
					},
				},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr(false),
					Containers: []corev1.Container{
						{
							Name:            "newt",
							Image:           r.NewtImage,
							ImagePullPolicy: corev1.PullIfNotPresent,
							SecurityContext: &corev1.SecurityContext{
								RunAsUser:                ptr(int64(0)),
								AllowPrivilegeEscalation: ptr(true),
								ReadOnlyRootFilesystem:   ptr(false),
							},
							Env: []corev1.EnvVar{
								{
									Name: "PANGOLIN_ENDPOINT",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: secretName,
											},
											Key: "PANGOLIN_ENDPOINT",
										},
									},
								},
								{
									Name: "NEWT_ID",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: secretName,
											},
											Key: "NEWT_ID",
										},
									},
								},
								{
									Name: "NEWT_SECRET",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: secretName,
											},
											Key: "NEWT_SECRET",
										},
									},
								},
								{
									Name:  "LOG_LEVEL",
									Value: "INFO",
								},
								{
									Name:  "HEALTH_FILE",
									Value: "/tmp/healthy",
								},
							},
							Ports: []corev1.ContainerPort{
								{
									Name:          "wg",
									ContainerPort: NewtWireGuardPort,
									Protocol:      corev1.ProtocolUDP,
								},
								{
									Name:          "tester",
									ContainerPort: NewtTesterPort,
									Protocol:      corev1.ProtocolUDP,
								},
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									Exec: &corev1.ExecAction{
										Command: []string{"test", "-f", "/tmp/healthy"},
									},
								},
								InitialDelaySeconds: 10,
								PeriodSeconds:       15,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									Exec: &corev1.ExecAction{
										Command: []string{"test", "-f", "/tmp/healthy"},
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       10,
								FailureThreshold:    3,
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:              resource.MustParse("200m"),
									corev1.ResourceMemory:           resource.MustParse("256Mi"),
									corev1.ResourceEphemeralStorage: resource.MustParse("256Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:              resource.MustParse("100m"),
									corev1.ResourceMemory:           resource.MustParse("128Mi"),
									corev1.ResourceEphemeralStorage: resource.MustParse("128Mi"),
								},
							},
						},
					},
				},
			},
		},
	}
}

// buildNewtService creates a LoadBalancer Service for the newt instance
func (r *NewtReconciler) buildNewtService(gateway *gatewayv1.Gateway, site *pangolin.Site) *corev1.Service {
	serviceName := fmt.Sprintf("%s-newt", gateway.Name)

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: gateway.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "newt",
				"app.kubernetes.io/instance":   gateway.Name,
				"app.kubernetes.io/managed-by": "pangolin-gateway-controller",
				NewtServiceLabel:               gateway.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: gateway.APIVersion,
					Kind:       gateway.Kind,
					Name:       gateway.Name,
					UID:        gateway.UID,
					Controller: ptr(true),
				},
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Selector: map[string]string{
				"app.kubernetes.io/instance": gateway.Name,
				"newt.instance":              "main",
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "wg",
					Port:       NewtWireGuardPort,
					TargetPort: intstr.FromInt(NewtWireGuardPort),
					Protocol:   corev1.ProtocolUDP,
				},
				{
					Name:       "tester",
					Port:       NewtTesterPort,
					TargetPort: intstr.FromInt(NewtTesterPort),
					Protocol:   corev1.ProtocolUDP,
				},
			},
		},
	}
}

// Helper functions to create or update resources
// createOrUpdateSecret creates a new Secret or updates it if it already exists
func (r *NewtReconciler) createOrUpdateSecret(ctx context.Context, secret *corev1.Secret) error {
	existing := &corev1.Secret{}
	err := r.Get(ctx, client.ObjectKey{Name: secret.Name, Namespace: secret.Namespace}, existing)
	if err != nil {
		if errors.IsNotFound(err) {
			return r.Create(ctx, secret)
		}
		return err
	}
	// Update existing secret
	existing.StringData = secret.StringData
	return r.Update(ctx, existing)
}

// createOrUpdateDeployment creates a new Deployment or updates it if it already exists
func (r *NewtReconciler) createOrUpdateDeployment(ctx context.Context, deployment *appsv1.Deployment) error {
	existing := &appsv1.Deployment{}
	err := r.Get(ctx, client.ObjectKey{Name: deployment.Name, Namespace: deployment.Namespace}, existing)
	if err != nil {
		if errors.IsNotFound(err) {
			return r.Create(ctx, deployment)
		}
		return err
	}
	// Update existing deployment
	existing.Spec = deployment.Spec
	return r.Update(ctx, existing)
}

// createOrUpdateService creates a new Service or updates it if it already exists
func (r *NewtReconciler) createOrUpdateService(ctx context.Context, service *corev1.Service) error {
	existing := &corev1.Service{}
	err := r.Get(ctx, client.ObjectKey{Name: service.Name, Namespace: service.Namespace}, existing)
	if err != nil {
		if errors.IsNotFound(err) {
			return r.Create(ctx, service)
		}
		return err
	}
	// Update existing service (preserve ClusterIP)
	service.Spec.ClusterIP = existing.Spec.ClusterIP
	existing.Spec = service.Spec
	return r.Update(ctx, existing)
}

// SetupWithManager registers the controller with the manager to watch Gateway resources.
// The newt controller watches Gateways and also owns Secrets, Deployments, and Services.
func (r *NewtReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorderFor("gateway-newt-controller")
	return ctrl.NewControllerManagedBy(mgr).
		Named("gateway-newt").
		For(&gatewayv1.Gateway{}).
		Owns(&corev1.Secret{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}

// Helper function to create pointer
func ptr[T any](v T) *T {
	return &v
}
