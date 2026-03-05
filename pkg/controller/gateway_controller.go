// Package controller implements Kubernetes controllers for the Pangolin Gateway integration.
// The GatewayReconciler manages Gateway API resources and creates corresponding Pangolin sites.
package controller

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/metrics"
	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	// FinalizerName is the finalizer added to Gateways to ensure cleanup
	FinalizerName = "gateway.pangolin.net/finalizer"

	// ResourceIDLabel stores the Pangolin resource ID (unused in Integration API)
	ResourceIDLabel = "gateway.pangolin.net/resource-id"

	// SiteIDLabel stores the Pangolin site ID for the Gateway
	SiteIDLabel = "gateway.pangolin.net/site-id"

	// ControllerName is the identifier for this controller in status updates
	ControllerName = "pangol.in/gateway-controller"

	// GatewayClassName is the GatewayClass this controller manages
	GatewayClassName = "pangolin"

	// ReconcileTimeout is the maximum time allowed for a reconciliation
	ReconcileTimeout = 30 * time.Second
)

// GatewayReconciler reconciles Gateway API resources and manages their
// corresponding Pangolin newt-type sites. It creates sites with names
// following the pattern "pgc-{gateway-name}" and stores credentials
// in Secrets for the newt controller to use.
type GatewayReconciler struct {
	client.Client
	Log             logr.Logger
	Scheme          *runtime.Scheme
	PangolinClient  pangolin.ClientInterface
	ControllerClass string
	Recorder        record.EventRecorder
}

// Reconcile implements the main reconciliation loop for Gateway resources.
// It ensures that a Pangolin newt site exists for each Gateway and stores
// the site credentials in a Secret for the newt VPN pod.
//
// Returns:
//   - ctrl.Result: Indicates if/when reconciliation should be retried
//   - error: Any error encountered during reconciliation
func (r *GatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	log := r.Log.WithValues("gateway", req.NamespacedName)
	log.Info("Reconciling Gateway")

	// Track reconciliation metrics
	defer func() {
		duration := time.Since(startTime).Seconds()
		metrics.ReconcileDuration.WithLabelValues("gateway").Observe(duration)
	}()

	ctx, cancel := context.WithTimeout(ctx, ReconcileTimeout)
	defer cancel()

	// Fetch the Gateway instance
	gateway := &gatewayv1.Gateway{}
	if err := r.Get(ctx, req.NamespacedName, gateway); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Gateway resource not found, likely deleted")
			metrics.ReconcileTotal.WithLabelValues("gateway", "not_found").Inc()
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get Gateway")
		metrics.ReconcileTotal.WithLabelValues("gateway", "error").Inc()
		return ctrl.Result{}, err
	}

	// Check if the Gateway is using our GatewayClass
	if string(gateway.Spec.GatewayClassName) != r.ControllerClass {
		log.Info("Gateway is not using our GatewayClass, skipping", "class", gateway.Spec.GatewayClassName)
		metrics.ReconcileTotal.WithLabelValues("gateway", "skipped").Inc()
		return ctrl.Result{}, nil
	}

	// Track Gateway count
	metrics.GatewayTotal.WithLabelValues(gateway.Namespace).Set(1)

	// Handle deletion
	if !gateway.ObjectMeta.DeletionTimestamp.IsZero() {
		result, err := r.handleDelete(ctx, gateway, log)
		if err != nil {
			metrics.ReconcileTotal.WithLabelValues("gateway", "error").Inc()
		} else {
			metrics.ReconcileTotal.WithLabelValues("gateway", "deleted").Inc()
			metrics.GatewayTotal.WithLabelValues(gateway.Namespace).Set(0)
		}
		return result, err
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(gateway, FinalizerName) {
		controllerutil.AddFinalizer(gateway, FinalizerName)
		if err := r.Update(ctx, gateway); err != nil {
			log.Error(err, "Failed to add finalizer")
			metrics.ReconcileTotal.WithLabelValues("gateway", "error").Inc()
			return ctrl.Result{}, err
		}
	}

	// Reconcile the Gateway
	result, err := r.reconcileGateway(ctx, gateway, log)
	if err != nil {
		metrics.ReconcileTotal.WithLabelValues("gateway", "error").Inc()
	} else if result.Requeue || result.RequeueAfter > 0 {
		metrics.ReconcileTotal.WithLabelValues("gateway", "requeue").Inc()
	} else {
		metrics.ReconcileTotal.WithLabelValues("gateway", "success").Inc()
	}
	return result, err
}

// handleDelete handles the deletion of a Gateway resource by cleaning up
// Pangolin resources and removing the finalizer.
func (r *GatewayReconciler) handleDelete(ctx context.Context, gateway *gatewayv1.Gateway, log logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(gateway, FinalizerName) {
		return ctrl.Result{}, nil
	}

	log.Info("Deleting Gateway from Pangolin")

	// Get the site ID from labels
	siteIDStr := gateway.Labels[SiteIDLabel]
	if siteIDStr != "" {
		siteID, err := strconv.Atoi(siteIDStr)
		if err != nil {
			log.Error(err, "Failed to parse site ID", "siteID", siteIDStr)
		} else {
			// Delete the site from Pangolin
			if err := r.PangolinClient.DeleteSite(ctx, siteID); err != nil {
				log.Error(err, "Failed to delete site from Pangolin", "siteID", siteID)
				// Continue anyway, the site might already be deleted
			} else {
				log.Info("Deleted site from Pangolin", "siteID", siteID)
				if r.Recorder != nil {
					r.Recorder.Event(gateway, corev1.EventTypeNormal, "SiteDeleted",
						fmt.Sprintf("Deleted Pangolin site %d", siteID))
				}
			}
		}
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(gateway, FinalizerName)
	if err := r.Update(ctx, gateway); err != nil {
		log.Error(err, "Failed to remove finalizer")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// reconcileGateway ensures a Pangolin newt site exists for the Gateway and
// creates a Secret with newt credentials. The newt controller will use this
// Secret to deploy a VPN pod that establishes a WireGuard tunnel to Pangolin.
func (r *GatewayReconciler) reconcileGateway(ctx context.Context, gateway *gatewayv1.Gateway, log logr.Logger) (ctrl.Result, error) {
	// Get or create the Pangolin site
	siteID := gateway.Labels[SiteIDLabel]

	if siteID == "" {
		// Create new site with newt configuration
		site, err := r.ensureSite(ctx, gateway, log)
		if err != nil {
			log.Error(err, "Failed to ensure site")
			r.updateGatewayStatus(ctx, gateway, gatewayv1.GatewayConditionProgrammed, false, "SiteError", err.Error())
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}

		// Create Secret with newt credentials
		if err := r.createNewtCredentialsSecret(ctx, gateway, site); err != nil {
			log.Error(err, "Failed to create newt credentials secret")
			return ctrl.Result{}, err
		}

		// Update Gateway labels with site ID
		if gateway.Labels == nil {
			gateway.Labels = make(map[string]string)
		}
		gateway.Labels[SiteIDLabel] = fmt.Sprintf("%d", site.ID)

		if err := r.Update(ctx, gateway); err != nil {
			log.Error(err, "Failed to update Gateway labels")
			return ctrl.Result{}, err
		}

		siteID = fmt.Sprintf("%d", site.ID)
		log.Info("Gateway configured with Pangolin site", "siteID", siteID)
		if r.Recorder != nil {
			r.Recorder.Event(gateway, corev1.EventTypeNormal, "Programmed",
				fmt.Sprintf("Gateway programmed with Pangolin site %s", siteID))
		}
	} else {
		// Verify site still exists in Pangolin, recreate if deleted
		if err := r.verifyOrRecreateSite(ctx, gateway, siteID, log); err != nil {
			log.Error(err, "Failed to verify/recreate site")
			r.updateGatewayStatus(ctx, gateway, gatewayv1.GatewayConditionProgrammed, false, "SiteVerificationFailed", err.Error())
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}
	}

	// Update Gateway status
	r.updateGatewayStatus(ctx, gateway, gatewayv1.GatewayConditionProgrammed, true, "Programmed", "Gateway is programmed in Pangolin")
	r.updateGatewayStatus(ctx, gateway, gatewayv1.GatewayConditionAccepted, true, "Accepted", "Gateway has been accepted")

	log.Info("Successfully reconciled Gateway", "siteID", siteID)
	// Requeue after 5 minutes to periodically verify site still exists
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// ensureSite ensures a Pangolin newt-type site exists for the Gateway.
// It first checks if a site with the name "pgc-{gateway-name}" already exists.
// If not, it uses the PickSiteDefaults API to get auto-allocated configuration
// (subnet, exit node, credentials) and creates a new site.
//
// Returns:
//   - *pangolin.Site: The site object with ID and credentials
//   - error: Any error encountered
func (r *GatewayReconciler) ensureSite(ctx context.Context, gateway *gatewayv1.Gateway, log logr.Logger) (*pangolin.Site, error) {
	// Create or find site based on gateway name
	sites, err := r.PangolinClient.ListSites(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list sites: %w", err)
	}

	// Look for existing site for this gateway
	siteName := fmt.Sprintf("pgc-%s", gateway.Name)
	for _, site := range sites {
		if site.Name == siteName {
			// Site exists, credentials should be in Secret already
			return &pangolin.Site{
				ID:   site.ID,
				Name: site.Name,
			}, nil
		}
	}

	// Create new site with newt configuration
	// Get default values from Pangolin API
	defaults, err := r.PangolinClient.PickSiteDefaults(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get site defaults: %w", err)
	}

	site := &pangolin.Site{
		Name:       siteName,
		Type:       "newt",
		Subnet:     defaults.Subnet,
		Address:    defaults.ClientAddress,
		ExitNodeID: defaults.ExitNodeID,
		Secret:     defaults.NewtSecret,
		NewtID:     defaults.NewtID,
	}

	createdSite, err := r.PangolinClient.CreateSite(ctx, site)
	if err != nil {
		return nil, fmt.Errorf("failed to create site: %w", err)
	}

	// Preserve credentials from PickSiteDefaults (API doesn't return them)
	createdSite.NewtID = site.NewtID
	createdSite.Secret = site.Secret

	log.Info("Created new Pangolin site", "siteID", createdSite.ID, "siteName", siteName, "newtID", createdSite.NewtID)
	if r.Recorder != nil {
		r.Recorder.Event(gateway, corev1.EventTypeNormal, "SiteCreated",
			fmt.Sprintf("Created Pangolin site %d (%s)", createdSite.ID, siteName))
	}
	return createdSite, nil
}

// verifyOrRecreateSite checks if the site exists in Pangolin and recreates if deleted
func (r *GatewayReconciler) verifyOrRecreateSite(ctx context.Context, gateway *gatewayv1.Gateway, siteIDStr string, log logr.Logger) error {
	siteID, err := strconv.Atoi(siteIDStr)
	if err != nil {
		return fmt.Errorf("invalid site ID %s: %w", siteIDStr, err)
	}

	// Try to list sites and verify this site exists
	sites, err := r.PangolinClient.ListSites(ctx)
	if err != nil {
		return fmt.Errorf("failed to list sites: %w", err)
	}

	// Check if site exists
	for _, site := range sites {
		if site.ID == siteID {
			// Site exists, all good
			return nil
		}
	}

	// Site doesn't exist, recreate it
	log.Info("Site not found in Pangolin, recreating", "siteID", siteID)
	newSite, err := r.ensureSite(ctx, gateway, log)
	if err != nil {
		return fmt.Errorf("failed to recreate site: %w", err)
	}

	// Update Gateway labels with new site ID
	if gateway.Labels == nil {
		gateway.Labels = make(map[string]string)
	}
	gateway.Labels[SiteIDLabel] = fmt.Sprintf("%d", newSite.ID)

	// Update Secret with new credentials
	if err := r.createNewtCredentialsSecret(ctx, gateway, newSite); err != nil {
		return fmt.Errorf("failed to update credentials secret: %w", err)
	}

	if err := r.Update(ctx, gateway); err != nil {
		return fmt.Errorf("failed to update Gateway with new site ID: %w", err)
	}

	log.Info("Recreated site after deletion", "oldSiteID", siteID, "newSiteID", newSite.ID)
	if r.Recorder != nil {
		r.Recorder.Event(gateway, corev1.EventTypeWarning, "SiteRecreated",
			fmt.Sprintf("Pangolin site %d was missing, recreated as %d", siteID, newSite.ID))
	}
	return nil
}

// createNewtCredentialsSecret creates a Kubernetes Secret containing the
// newt VPN credentials (NEWT_ID and NEWT_SECRET). This Secret is owned by
// the Gateway and will be automatically deleted when the Gateway is deleted.
// The newt controller watches for this Secret to deploy the VPN pod.
func (r *GatewayReconciler) createNewtCredentialsSecret(ctx context.Context, gateway *gatewayv1.Gateway, site *pangolin.Site) error {
	secretName := fmt.Sprintf("%s-newt-cred", gateway.Name)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: gateway.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "newt",
				"app.kubernetes.io/instance":   gateway.Name,
				"app.kubernetes.io/managed-by": "pangolin-gateway-controller",
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
			"NEWT_ID":     site.NewtID,
			"NEWT_SECRET": site.Secret,
		},
	}

	if err := r.Create(ctx, secret); err != nil {
		if errors.IsAlreadyExists(err) {
			// Secret already exists, update it
			return r.Update(ctx, secret)
		}
		return fmt.Errorf("failed to create secret: %w", err)
	}

	return nil
}

// updateGatewayStatus updates the Gateway status condition with conflict retry
func (r *GatewayReconciler) updateGatewayStatus(ctx context.Context, gateway *gatewayv1.Gateway, condType gatewayv1.GatewayConditionType, status bool, reason, message string) {
	// Use retry.RetryOnConflict to handle resource conflicts
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Re-fetch the Gateway to get the latest version
		key := client.ObjectKeyFromObject(gateway)
		fresh := &gatewayv1.Gateway{}
		if err := r.Get(ctx, key, fresh); err != nil {
			return err
		}

		condition := metav1.Condition{
			Type:               string(condType),
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: fresh.Generation,
			LastTransitionTime: metav1.Now(),
		}

		if status {
			condition.Status = metav1.ConditionTrue
		}

		// Update or append condition
		found := false
		for i, cond := range fresh.Status.Conditions {
			if cond.Type == condition.Type {
				// Only update if condition actually changed
				if cond.Status != condition.Status || cond.Reason != condition.Reason {
					fresh.Status.Conditions[i] = condition
				} else {
					// No change needed
					return nil
				}
				found = true
				break
			}
		}

		if !found {
			fresh.Status.Conditions = append(fresh.Status.Conditions, condition)
		}

		// Use Status().Update() - separate from spec updates
		return r.Status().Update(ctx, fresh)
	})

	if err != nil {
		r.Log.Error(err, "Failed to update Gateway status after retries",
			"gateway", gateway.Name,
			"namespace", gateway.Namespace,
			"conditionType", condType)
	}
}

// SetupWithManager registers the controller with the manager to watch Gateway resources.
// Enables parallel reconciliation of multiple Gateways for improved throughput.
func (r *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorderFor("gateway-site-controller")
	return ctrl.NewControllerManagedBy(mgr).
		Named("gateway-site").
		For(&gatewayv1.Gateway{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 3, // Parallelize reconciliation across 3 Gateways
		}).
		Complete(r)
}
