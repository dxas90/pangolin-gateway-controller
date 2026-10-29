package controller

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/config"
	"github.com/dxas90/pangolin-gateway-controller/pkg/metrics"
	"github.com/dxas90/pangolin-gateway-controller/pkg/pangolin"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// TCPRouteReconciler reconciles TCPRoute resources, mapping each route to a
// Pangolin resource+target pair and exposing TCP services through the Pangolin
// reverse-proxy network.
type TCPRouteReconciler struct {
	client.Client
	Log            logr.Logger
	Scheme         *runtime.Scheme
	PangolinClient pangolin.ClientInterface
	Config         *config.ControllerConfig
	Recorder       record.EventRecorder
}

// Reconcile implements the reconciliation loop for TCPRoute resources.
func (r *TCPRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	startTime := time.Now()
	log := r.Log.WithValues("tcproute", req.NamespacedName)
	log.Info("Reconciling TCPRoute")

	defer func() {
		duration := time.Since(startTime).Seconds()
		metrics.ReconcileDuration.WithLabelValues("tcproute").Observe(duration)
	}()

	ctx, cancel := context.WithTimeout(ctx, ReconcileTimeout)
	defer cancel()

	route := &gatewayv1alpha2.TCPRoute{}
	if err := r.Get(ctx, req.NamespacedName, route); err != nil {
		if errors.IsNotFound(err) {
			log.Info("TCPRoute resource not found, likely deleted")
			metrics.ReconcileTotal.WithLabelValues("tcproute", "not_found").Inc()
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get TCPRoute")
		metrics.ReconcileTotal.WithLabelValues("tcproute", "error").Inc()
		return ctrl.Result{}, err
	}

	if !route.ObjectMeta.DeletionTimestamp.IsZero() {
		result, err := r.handleTCPDelete(ctx, route, log)
		if err != nil {
			metrics.ReconcileTotal.WithLabelValues("tcproute", "error").Inc()
		} else {
			metrics.ReconcileTotal.WithLabelValues("tcproute", "deleted").Inc()
		}
		return result, err
	}

	if !controllerutil.ContainsFinalizer(route, FinalizerName) {
		original := route.DeepCopy()
		controllerutil.AddFinalizer(route, FinalizerName)
		if err := r.Patch(ctx, route, client.MergeFrom(original)); err != nil {
			log.Error(err, "Failed to add finalizer")
			metrics.ReconcileTotal.WithLabelValues("tcproute", "error").Inc()
			return ctrl.Result{}, err
		}
	}

	result, err := r.reconcileTCPRoute(ctx, route, log)
	if err != nil {
		metrics.ReconcileTotal.WithLabelValues("tcproute", "error").Inc()
	} else if result.RequeueAfter > 0 {
		metrics.ReconcileTotal.WithLabelValues("tcproute", "requeue").Inc()
	} else {
		metrics.ReconcileTotal.WithLabelValues("tcproute", "success").Inc()
	}
	return result, err
}

// handleTCPDelete removes the Pangolin resource associated with this TCPRoute
// and strips the finalizer so Kubernetes can complete deletion.
func (r *TCPRouteReconciler) handleTCPDelete(ctx context.Context, route *gatewayv1alpha2.TCPRoute, log logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(route, FinalizerName) {
		return ctrl.Result{}, nil
	}

	log.Info("Deleting TCPRoute from Pangolin")

	resourceID := route.Labels[ResourceIDLabel]
	if resourceID != "" {
		log.Info("Deleting Pangolin resource", "resourceID", resourceID)
		if err := r.PangolinClient.DeleteResource(ctx, resourceID); err != nil {
			if apiErr, ok := pangolin.AsAPIError(err); ok && apiErr.StatusCode == 404 {
				log.Info("Resource already deleted (404), continuing with finalizer removal", "resourceID", resourceID)
			} else {
				log.Error(err, "Failed to delete resource from Pangolin, will retry", "resourceID", resourceID)
				return ctrl.Result{RequeueAfter: 30 * time.Second}, err
			}
		} else {
			log.Info("Successfully deleted Pangolin resource", "resourceID", resourceID)
		}
	} else {
		log.Info("No resource ID in labels, skipping Pangolin cleanup")
	}

	original := route.DeepCopy()
	controllerutil.RemoveFinalizer(route, FinalizerName)
	if err := r.Patch(ctx, route, client.MergeFrom(original)); err != nil {
		log.Error(err, "Failed to remove finalizer")
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileTCPRoute ensures a Pangolin resource and target exist for the route.
func (r *TCPRouteReconciler) reconcileTCPRoute(ctx context.Context, route *gatewayv1alpha2.TCPRoute, log logr.Logger) (ctrl.Result, error) {
	if len(route.Spec.ParentRefs) == 0 {
		log.Info("No parent gateway references found")
		r.updateTCPRouteStatus(ctx, route, false, "NoParentGateway", "No parent gateway references configured")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	parentRef := route.Spec.ParentRefs[0]
	gatewayName := string(parentRef.Name)
	gatewayNamespace := route.Namespace
	if parentRef.Namespace != nil {
		gatewayNamespace = string(*parentRef.Namespace)
	}

	gateway := &gatewayv1.Gateway{}
	if err := r.Get(ctx, types.NamespacedName{Name: gatewayName, Namespace: gatewayNamespace}, gateway); err != nil {
		log.Error(err, "Failed to get parent Gateway")
		r.updateTCPRouteStatus(ctx, route, false, "ParentGatewayNotFound", "Parent gateway not found")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	siteIDStr := gateway.Labels[SiteIDLabel]
	if siteIDStr == "" {
		log.Info("Gateway does not have site ID yet")
		r.updateTCPRouteStatus(ctx, route, false, "GatewayNotReady", "Parent gateway is not ready")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	siteID, err := strconv.Atoi(siteIDStr)
	if err != nil {
		log.Error(err, "Invalid site ID label on Gateway", "siteIDStr", siteIDStr)
		r.updateTCPRouteStatus(ctx, route, false, "InvalidSiteID", fmt.Sprintf("Invalid site ID: %s", siteIDStr))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Resource name: <namespace>/<name> — TCPRoute has no hostnames
	resourceName := fmt.Sprintf("%s/%s", route.Namespace, route.Name)

	resourceID := route.Labels[ResourceIDLabel]
	if resourceID == "" {
		newID, err := r.createTCPPangolinResource(ctx, route, gateway, resourceName, log)
		if err != nil {
			log.Error(err, "Failed to create Pangolin resource")
			r.updateTCPRouteStatus(ctx, route, false, "ResourceCreationFailed", sanitizeEventMessage(err))
			r.Recorder.Eventf(route, corev1.EventTypeWarning, "ResourceCreationFailed",
				"Failed to create Pangolin resource: %s", sanitizeEventMessage(err))
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}
		resourceID = newID

		original := route.DeepCopy()
		if route.Labels == nil {
			route.Labels = map[string]string{}
		}
		route.Labels[ResourceIDLabel] = resourceID
		if err := r.Patch(ctx, route, client.MergeFrom(original)); err != nil {
			log.Error(err, "Failed to patch resource ID label")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, err
		}
	}

	// Reconcile targets (one per backend rule)
	if err := r.reconcileTCPTargets(ctx, route, resourceID, siteID, log); err != nil {
		if isSiteGoneError(err) {
			log.Info("Site is gone, cleaning up route", "siteIDStr", siteIDStr)
			r.updateTCPRouteStatus(ctx, route, false, "SiteGone", "Pangolin site no longer exists")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		log.Error(err, "Failed to reconcile TCP targets")
		r.updateTCPRouteStatus(ctx, route, false, "TargetError", sanitizeEventMessage(err))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	r.updateTCPRouteStatus(ctx, route, true, "Accepted", "TCPRoute successfully reconciled")
	r.Recorder.Eventf(route, corev1.EventTypeNormal, "Reconciled", "TCPRoute %s/%s reconciled", route.Namespace, route.Name)
	return ctrl.Result{}, nil
}

// createTCPPangolinResource creates a new Pangolin resource for this TCPRoute.
// TCPRoutes use "tcp" mode, indicating a raw TCP passthrough tunnel.
func (r *TCPRouteReconciler) createTCPPangolinResource(
	ctx context.Context,
	route *gatewayv1alpha2.TCPRoute,
	gateway *gatewayv1.Gateway,
	resourceName string,
	log logr.Logger,
) (string, error) {
	siteIDStr := gateway.Labels[SiteIDLabel]
	siteID, err := strconv.Atoi(siteIDStr)
	if err != nil {
		return "", fmt.Errorf("invalid siteID %q: %w", siteIDStr, err)
	}

	payload := map[string]any{
		"name":   resourceName,
		"siteId": siteID,
		"mode":   "tcp",
	}

	result, err := r.PangolinClient.CreateResource(ctx, payload)
	if err != nil {
		return "", fmt.Errorf("failed to create Pangolin resource for TCPRoute %s/%s: %w",
			route.Namespace, route.Name, err)
	}

	resourceID, ok := result["resourceId"].(string)
	if !ok || resourceID == "" {
		return "", fmt.Errorf("Pangolin returned no resourceId for TCPRoute %s/%s", route.Namespace, route.Name)
	}

	log.Info("Created Pangolin resource for TCPRoute", "resourceID", resourceID, "resourceName", resourceName)
	return resourceID, nil
}

// reconcileTCPTargets creates Pangolin targets for each backend in the TCPRoute rules.
// Existing matching targets are preserved; stale targets are removed.
func (r *TCPRouteReconciler) reconcileTCPTargets(
	ctx context.Context,
	route *gatewayv1alpha2.TCPRoute,
	resourceID string,
	siteID int,
	log logr.Logger,
) error {
	existingTargets, err := r.PangolinClient.ListTargetsRaw(ctx, resourceID)
	if err != nil {
		return fmt.Errorf("failed to list targets: %w", err)
	}

	// Build the set of desired (ip, port) pairs
	type endpoint struct {
		ip   string
		port int
	}
	desired := map[endpoint]bool{}

	for _, rule := range route.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			if backendRef.Port == nil {
				log.Info("BackendRef missing Port, skipping", "backend", backendRef.Name)
				continue
			}
			svcNS := route.Namespace
			if backendRef.Namespace != nil {
				svcNS = string(*backendRef.Namespace)
			}
			svc := &corev1.Service{}
			if err := r.Get(ctx, types.NamespacedName{Name: string(backendRef.Name), Namespace: svcNS}, svc); err != nil {
				return fmt.Errorf("failed to get service %s: %w", backendRef.Name, err)
			}
			ip := svc.Spec.ClusterIP
			if ip == "" || ip == "None" {
				log.Info("Service has no ClusterIP, skipping", "service", backendRef.Name)
				continue
			}
			desired[endpoint{ip: ip, port: int(*backendRef.Port)}] = true
		}
	}

	// Match existing targets
	matchedIDs := map[string]bool{}
	for _, t := range existingTargets {
		tIP, _ := t["ip"].(string)
		tPort, _ := t["port"].(float64)
		tIDFloat, ok := t["targetId"].(float64)
		if !ok {
			continue
		}
		tID := fmt.Sprintf("%.0f", tIDFloat)
		ep := endpoint{ip: tIP, port: int(tPort)}
		if desired[ep] {
			matchedIDs[tID] = true
		}
	}

	// Create missing targets
	for ep := range desired {
		found := false
		for _, t := range existingTargets {
			tIP, _ := t["ip"].(string)
			tPort, _ := t["port"].(float64)
			if tIP == ep.ip && int(tPort) == ep.port {
				found = true
				break
			}
		}
		if found {
			continue
		}
		payload := map[string]any{
			"ip":     ep.ip,
			"port":   ep.port,
			"siteId": siteID,
			"method": "tcp",
		}
		if _, err := r.PangolinClient.CreateTargetRaw(ctx, resourceID, payload); err != nil {
			return fmt.Errorf("failed to create target %s:%d: %w", ep.ip, ep.port, err)
		}
		log.Info("Created TCP target", "ip", ep.ip, "port", ep.port, "resourceID", resourceID)
	}

	// Remove orphaned targets
	for _, t := range existingTargets {
		tIDFloat, ok := t["targetId"].(float64)
		if !ok {
			continue
		}
		tID := fmt.Sprintf("%.0f", tIDFloat)
		if !matchedIDs[tID] {
			if err := r.PangolinClient.DeleteTarget(ctx, tID); err != nil {
				log.Error(err, "Failed to delete orphaned TCP target", "targetId", tID)
			} else {
				log.Info("Deleted orphaned TCP target", "targetId", tID)
			}
		}
	}
	return nil
}

// updateTCPRouteStatus writes a RouteParentStatus condition to the TCPRoute.
func (r *TCPRouteReconciler) updateTCPRouteStatus(
	ctx context.Context,
	route *gatewayv1alpha2.TCPRoute,
	accepted bool,
	reason, message string,
) {
	if len(route.Spec.ParentRefs) == 0 {
		return
	}
	key := client.ObjectKeyFromObject(route)
	parentRef := route.Spec.ParentRefs[0]

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &gatewayv1alpha2.TCPRoute{}
		if err := r.Get(ctx, key, current); err != nil {
			return err
		}
		condition := metav1.Condition{
			Type:               string(gatewayv1.RouteConditionAccepted),
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: current.Generation,
			LastTransitionTime: metav1.Now(),
		}
		if accepted {
			condition.Status = metav1.ConditionTrue
		}
		// Preserve LastTransitionTime if status hasn't changed for this parent
		for _, ps := range current.Status.Parents {
			if ps.ParentRef.Name == parentRef.Name {
				for _, existing := range ps.Conditions {
					if existing.Type == condition.Type && existing.Status == condition.Status {
						condition.LastTransitionTime = existing.LastTransitionTime
						break
					}
				}
				break
			}
		}
		newParent := gatewayv1.RouteParentStatus{
			ParentRef:      gatewayv1.ParentReference(parentRef),
			ControllerName: gatewayv1.GatewayController(ControllerName),
			Conditions:     []metav1.Condition{condition},
		}
		found := false
		for i, ps := range current.Status.Parents {
			if ps.ParentRef.Name == newParent.ParentRef.Name {
				current.Status.Parents[i] = newParent
				found = true
				break
			}
		}
		if !found {
			current.Status.Parents = append(current.Status.Parents, newParent)
		}
		return r.Status().Update(ctx, current)
	})
	if err != nil {
		r.Log.Error(err, "Failed to update TCPRoute status")
	}
}

// SetupWithManager registers the TCPRoute controller with the manager.
func (r *TCPRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gatewayv1alpha2.TCPRoute{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("tcproute").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.Config.MaxConcurrentReconciles,
		}).
		Complete(r)
}
