package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/config"
	"github.com/dxas90/pangolin-gateway-controller/pkg/metrics"
	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// HTTPRouteReconciler reconciles HTTPRoute resources
type HTTPRouteReconciler struct {
	client.Client
	Log            logr.Logger
	Scheme         *runtime.Scheme
	PangolinClient pangolin.ClientInterface
	Config         *config.ControllerConfig
	Recorder       record.EventRecorder
}

// Reconcile implements the reconciliation logic for HTTPRoute resources
func (r *HTTPRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	log := r.Log.WithValues("httproute", req.NamespacedName)
	log.Info("Reconciling HTTPRoute")

	// Track reconciliation duration
	defer func() {
		duration := time.Since(startTime).Seconds()
		metrics.ReconcileDuration.WithLabelValues("httproute").Observe(duration)
	}()

	ctx, cancel := context.WithTimeout(ctx, ReconcileTimeout)
	defer cancel()

	route := &gatewayv1.HTTPRoute{}
	if err := r.Get(ctx, req.NamespacedName, route); err != nil {
		if errors.IsNotFound(err) {
			log.V(1).Info("HTTPRoute resource not found, likely deleted")
			metrics.ReconcileTotal.WithLabelValues("httproute", "not_found").Inc()
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get HTTPRoute")
		metrics.ReconcileTotal.WithLabelValues("httproute", "error").Inc()
		return ctrl.Result{}, err
	}

	if !route.ObjectMeta.DeletionTimestamp.IsZero() {
		result, err := r.handleDelete(ctx, route, log)
		if err != nil {
			metrics.ReconcileTotal.WithLabelValues("httproute", "error").Inc()
		} else {
			metrics.ReconcileTotal.WithLabelValues("httproute", "deleted").Inc()
		}
		return result, err
	}

	if !controllerutil.ContainsFinalizer(route, FinalizerName) {
		original := route.DeepCopy()
		controllerutil.AddFinalizer(route, FinalizerName)
		if err := r.Patch(ctx, route, client.MergeFrom(original)); err != nil {
			log.Error(err, "Failed to add finalizer")
			metrics.ReconcileTotal.WithLabelValues("httproute", "error").Inc()
			return ctrl.Result{}, err
		}
	}

	result, err := r.reconcileHTTPRoute(ctx, route, log)
	if err != nil {
		metrics.ReconcileTotal.WithLabelValues("httproute", "error").Inc()
	} else if result.Requeue || result.RequeueAfter > 0 {
		metrics.ReconcileTotal.WithLabelValues("httproute", "requeue").Inc()
	} else {
		metrics.ReconcileTotal.WithLabelValues("httproute", "success").Inc()
	}
	return result, err
}

// handleDelete handles the deletion of an HTTPRoute resource.
// Only deletes targets belonging to this HTTPRoute, not the whole resource.
// Deletes the resource only if no targets remain.
func (r *HTTPRouteReconciler) handleDelete(ctx context.Context, route *gatewayv1.HTTPRoute, log logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(route, FinalizerName) {
		return ctrl.Result{}, nil
	}

	log.Info("Deleting HTTPRoute targets from Pangolin")

	// Fetch all resources once before the loop to avoid an N+1 ListResources call.
	allResources, err := r.PangolinClient.ListResources(ctx)
	if err != nil {
		log.Error(err, "Failed to list Pangolin resources for deletion cleanup")
		// Continue with an empty map — don't block deletion entirely.
	}
	resourcesByName := make(map[string]string)
	for _, res := range allResources {
		if name, ok := res["name"].(string); ok {
			if id := fmt.Sprintf("%v", res["resourceId"]); id != "" && id != "<nil>" {
				resourcesByName[name] = id
			}
		}
	}

	var deletionErrors []error

	for _, hostname := range route.Spec.Hostnames {
		select {
		case <-ctx.Done():
			return ctrl.Result{}, ctx.Err()
		default:
		}

		resourceID := resourcesByName[string(hostname)]
		if resourceID == "" {
			log.Info("Resource not found, skipping target deletion", "hostname", hostname)
			continue
		}

		existingTargets, err := r.PangolinClient.ListTargetsRaw(ctx, resourceID)
		if err != nil {
			log.Error(err, "Failed to list targets for deletion", "resourceID", resourceID)
			continue
		}

		var remainingTargets []map[string]interface{}
		for _, target := range existingTargets {
			select {
			case <-ctx.Done():
				return ctrl.Result{}, ctx.Err()
			default:
			}

			targetIDFloat, ok := target["targetId"].(float64)
			if !ok {
				remainingTargets = append(remainingTargets, target)
				continue
			}
			targetID := fmt.Sprintf("%.0f", targetIDFloat)

			shouldDelete := false
			if labels, ok := target["labels"].(map[string]interface{}); ok {
				if routeName, ok := labels["gateway.pangolin.net/httproute-name"].(string); ok {
					if routeNamespace, ok := labels["gateway.pangolin.net/httproute-namespace"].(string); ok {
						if routeName == route.Name && routeNamespace == route.Namespace {
							shouldDelete = true
						}
					}
				}
			} else {
				// Legacy targets without labels - delete all for safety
				shouldDelete = true
				log.V(1).Info("Target has no ownership labels, deleting (legacy target)", "targetId", targetID)
			}

			if !shouldDelete {
				log.V(1).Info("Skipping target from different HTTPRoute", "targetId", targetID)
				remainingTargets = append(remainingTargets, target)
				continue
			}

			log.Info("Deleting target", "targetId", targetID, "resourceID", resourceID, "hostname", hostname)
			if err := r.PangolinClient.DeleteTarget(ctx, targetID); err != nil {
				log.Error(err, "Failed to delete target", "targetId", targetID)
				remainingTargets = append(remainingTargets, target)
				deletionErrors = append(deletionErrors, fmt.Errorf("target %s: %w", targetID, err))
			}
		}

		deletedCount := len(existingTargets) - len(remainingTargets)
		log.Info("Deleted targets for hostname", "hostname", hostname, "resourceID", resourceID, "count", deletedCount)

		if len(remainingTargets) == 0 {
			log.Info("No targets remain, deleting resource", "resourceID", resourceID, "hostname", hostname)
			if err := r.PangolinClient.DeleteResource(ctx, resourceID); err != nil {
				log.Error(err, "Failed to delete resource", "resourceID", resourceID)
				deletionErrors = append(deletionErrors, fmt.Errorf("resource %s: %w", resourceID, err))
			} else {
				log.Info("Successfully deleted resource", "resourceID", resourceID)
			}
		}
	}

	if len(deletionErrors) > 0 {
		log.Info("Some Pangolin resources could not be deleted, will retry", "errorCount", len(deletionErrors))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, fmt.Errorf("failed to delete %d Pangolin resource(s), will retry", len(deletionErrors))
	}

	original := route.DeepCopy()
	controllerutil.RemoveFinalizer(route, FinalizerName)
	if err := r.Patch(ctx, route, client.MergeFrom(original)); err != nil {
		log.Error(err, "Failed to remove finalizer")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// reconcileHTTPRoute reconciles the HTTPRoute with Pangolin
func (r *HTTPRouteReconciler) reconcileHTTPRoute(ctx context.Context, route *gatewayv1.HTTPRoute, log logr.Logger) (ctrl.Result, error) {
	if len(route.Spec.ParentRefs) == 0 {
		log.Info("No parent gateway references found")
		r.updateRouteStatus(ctx, route, false, "NoParentGateway", "No parent gateway references configured")
		return ctrl.Result{}, nil
	}

	parentRef := route.Spec.ParentRefs[0]
	gatewayName := string(parentRef.Name)
	gatewayNamespace := route.Namespace
	if parentRef.Namespace != nil {
		gatewayNamespace = string(*parentRef.Namespace)
	}

	gateway := &gatewayv1.Gateway{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      gatewayName,
		Namespace: gatewayNamespace,
	}, gateway); err != nil {
		log.Error(err, "Failed to get parent Gateway")
		r.updateRouteStatus(ctx, route, false, "ParentGatewayNotFound", "Parent gateway not found")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	siteIDStr := gateway.Labels[SiteIDLabel]
	if siteIDStr == "" {
		log.Info("Gateway does not have site ID yet")
		r.updateRouteStatus(ctx, route, false, "GatewayNotReady", "Parent gateway is not ready")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if len(route.Spec.Hostnames) == 0 {
		log.Info("No hostnames specified, cannot create resources")
		r.updateRouteStatus(ctx, route, false, "NoHostnames", "HTTPRoute has no hostnames configured")
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	// Cache domain list once for the entire reconciliation
	domains, err := r.PangolinClient.ListDomains(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list domains: %w", err)
	}

	// Cache resource list once and build a name→resourceID map
	allResources, err := r.PangolinClient.ListResources(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list resources: %w", err)
	}
	// Update gauge with current total Pangolin resources
	metrics.PangolinResources.Set(float64(len(allResources)))

	// Update gauge with current HTTPRoute count in this namespace
	var routeList gatewayv1.HTTPRouteList
	if listErr := r.List(ctx, &routeList, client.InNamespace(route.Namespace)); listErr == nil {
		metrics.HTTPRouteTotal.WithLabelValues(route.Namespace).Set(float64(len(routeList.Items)))
	}

	resourcesByName := make(map[string]string)
	for _, res := range allResources {
		if name, ok := res["name"].(string); ok {
			if id := fmt.Sprintf("%v", res["resourceId"]); id != "" && id != "<nil>" {
				resourcesByName[name] = id
			}
		}
	}

	processedHostnames := 0
	for _, hostname := range route.Spec.Hostnames {
		_, _, err := extractSubdomainFromDomains(string(hostname), domains)
		if err != nil {
			log.Info("Skipping hostname that doesn't match any Pangolin domain", "hostname", hostname)
			continue
		}

		resourceID := r.findExistingResourceBySubdomainFromMap(string(hostname), resourcesByName)

		if resourceID == "" {
			resourceName := string(hostname)
			newResourceID, err := r.createPangolinResourceForHostname(ctx, route, string(hostname), resourceName, domains, log)
			if err != nil {
				log.Error(err, "Failed to create Pangolin resource", "hostname", hostname)
				r.updateRouteStatus(ctx, route, false, "ResourceCreationFailed", sanitizeEventMessage(err))
				r.Recorder.Eventf(route, corev1.EventTypeWarning, "ResourceCreationFailed", "Failed to create Pangolin resource for hostname %s: %s", hostname, sanitizeEventMessage(err))
				return ctrl.Result{RequeueAfter: 30 * time.Second}, err
			}
			resourceID = newResourceID
			log.Info("Created Pangolin resource", "resourceID", resourceID, "hostname", hostname, "resourceName", resourceName)
			r.Recorder.Eventf(route, corev1.EventTypeNormal, "ResourceCreated", "Created Pangolin resource %s for hostname %s", resourceID, hostname)
		} else {
			log.V(1).Info("Using existing Pangolin resource", "resourceID", resourceID, "hostname", hostname)
			// Verify resource config matches desired state (drift detection)
			if err := r.verifyOrUpdateResource(ctx, route, resourceID, string(hostname), allResources, domains, log); err != nil {
				log.Error(err, "Failed to verify resource drift", "resourceID", resourceID)
				// Non-fatal: continue to target reconciliation
			}
		}

		if err := r.reconcileTargets(ctx, route, resourceID, siteIDStr, log); err != nil {
			log.Error(err, "Failed to reconcile targets", "resourceID", resourceID, "hostname", hostname)
			r.updateRouteStatus(ctx, route, false, "TargetError", sanitizeEventMessage(err))
			r.Recorder.Eventf(route, corev1.EventTypeWarning, "TargetError", "Failed to reconcile targets for hostname %s: %s", hostname, sanitizeEventMessage(err))
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}
		processedHostnames++
	}

	if processedHostnames == 0 {
		log.Info("No hostnames matched Pangolin domains, HTTPRoute has no valid hostnames")
		r.updateRouteStatus(ctx, route, false, "NoMatchingDomains", "None of the hostnames match Pangolin domains")
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	r.updateRouteStatus(ctx, route, true, "Accepted", "HTTPRoute is configured in Pangolin")
	r.Recorder.Eventf(route, corev1.EventTypeNormal, "Accepted", "HTTPRoute configured in Pangolin (%d hostname(s))", processedHostnames)

	log.Info("Successfully reconciled HTTPRoute", "processedHostnames", processedHostnames, "totalHostnames", len(route.Spec.Hostnames))
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *HTTPRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("httproute").
		For(&gatewayv1.HTTPRoute{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.Config.MaxConcurrentReconciles,
			RateLimiter:             buildRateLimiter(r.Config),
		}).
		Complete(r)
}
