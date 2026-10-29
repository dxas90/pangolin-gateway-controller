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
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
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

	// Only reconcile routes whose parent Gateway is managed by this controller class.
	if string(gateway.Spec.GatewayClassName) != r.Config.GatewayClassName {
		log.V(1).Info("Parent gateway not managed by this controller, skipping",
			"gatewayClass", gateway.Spec.GatewayClassName)
		return ctrl.Result{}, nil
	}

	// Add finalizer after confirming the gateway is ours.
	if !controllerutil.ContainsFinalizer(route, FinalizerName) {
		original := route.DeepCopy()
		controllerutil.AddFinalizer(route, FinalizerName)
		if err := r.Patch(ctx, route, client.MergeFrom(original)); err != nil {
			log.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
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

	proxyPort := resolveListenerPort(gateway, parentRef)

	resourceID := route.Labels[ResourceIDLabel]
	if resourceID == "" {
		newID, err := r.createTCPPangolinResource(ctx, route, gateway, siteID, proxyPort, log)
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
		log.Info("Created Pangolin resource", "resourceID", resourceID)
		r.Recorder.Eventf(route, corev1.EventTypeNormal, "ResourceCreated", "Created Pangolin resource %s", resourceID)
	} else {
		if err := r.verifyOrRecreateTCPResource(ctx, route, gateway, siteID, proxyPort, resourceID, log); err != nil {
			log.Error(err, "Failed to verify/recreate resource")
			r.updateTCPRouteStatus(ctx, route, false, "ResourceVerificationFailed", sanitizeEventMessage(err))
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}
		if updated := route.Labels[ResourceIDLabel]; updated != "" {
			resourceID = updated
		}
	}

	if err := r.reconcileTCPTargets(ctx, route, resourceID, siteID, log); err != nil {
		if isSiteGoneError(err) {
			log.Info("Site deleted from Pangolin, clearing gateway site-id to trigger immediate recreation", "siteID", siteIDStr)
			r.Recorder.Eventf(route, corev1.EventTypeWarning, "SiteGone", "Pangolin site %s was deleted; triggering gateway reconcile", siteIDStr)
			original := gateway.DeepCopy()
			delete(gateway.Labels, SiteIDLabel)
			if patchErr := r.Patch(ctx, gateway, client.MergeFrom(original)); patchErr != nil {
				log.Error(patchErr, "Failed to clear gateway site-id label")
			}
			r.updateTCPRouteStatus(ctx, route, false, "SiteGone", "Parent gateway site was deleted; waiting for recreation")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		log.Error(err, "Failed to reconcile TCP targets")
		r.updateTCPRouteStatus(ctx, route, false, "TargetError", sanitizeEventMessage(err))
		r.Recorder.Eventf(route, corev1.EventTypeWarning, "TargetError", "Failed to reconcile targets: %s", sanitizeEventMessage(err))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	r.updateTCPRouteStatus(ctx, route, true, "Accepted", "TCPRoute is configured in Pangolin")
	r.Recorder.Eventf(route, corev1.EventTypeNormal, "Accepted", "TCPRoute configured in Pangolin")
	log.Info("Successfully reconciled TCPRoute", "resourceID", resourceID)
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// resolveListenerPort returns the port number from the Gateway listener that
// matches parentRef.SectionName, falling back to the first listener's port.
func resolveListenerPort(gateway *gatewayv1.Gateway, parentRef gatewayv1.ParentReference) int {
	if parentRef.SectionName != nil {
		for _, listener := range gateway.Spec.Listeners {
			if listener.Name == *parentRef.SectionName {
				return int(listener.Port)
			}
		}
	}
	if len(gateway.Spec.Listeners) > 0 {
		return int(gateway.Spec.Listeners[0].Port)
	}
	return 0
}

// createTCPPangolinResource creates a new Pangolin resource for this TCPRoute.
// TCPRoutes use "tcp" mode, indicating a raw TCP passthrough tunnel.
func (r *TCPRouteReconciler) createTCPPangolinResource(
	ctx context.Context,
	route *gatewayv1alpha2.TCPRoute,
	gateway *gatewayv1.Gateway,
	siteID int,
	proxyPort int,
	log logr.Logger,
) (string, error) {
	resourceName := fmt.Sprintf("%s-%s", route.Namespace, route.Name)

	payload := map[string]any{
		"name":   resourceName,
		"siteId": siteID,
		"mode":   "tcp",
	}
	if proxyPort > 0 {
		payload["proxyPort"] = proxyPort
	}

	result, err := r.PangolinClient.CreateResource(ctx, payload)
	if err != nil {
		return "", fmt.Errorf("failed to create Pangolin resource for TCPRoute %s/%s: %w",
			route.Namespace, route.Name, err)
	}

	rawID := result["resourceId"]
	if rawID == nil {
		return "", fmt.Errorf("Pangolin returned no resourceId for TCPRoute %s/%s", route.Namespace, route.Name)
	}
	resourceID := fmt.Sprintf("%v", rawID)
	if resourceID == "" || resourceID == "<nil>" {
		return "", fmt.Errorf("Pangolin returned empty resourceId for TCPRoute %s/%s", route.Namespace, route.Name)
	}

	log.Info("Created Pangolin resource for TCPRoute", "resourceID", resourceID, "resourceName", resourceName)
	return resourceID, nil
}

// verifyOrRecreateTCPResource checks if the resource exists in Pangolin and
// recreates it if it was deleted externally (drift detection/recovery).
func (r *TCPRouteReconciler) verifyOrRecreateTCPResource(
	ctx context.Context,
	route *gatewayv1alpha2.TCPRoute,
	gateway *gatewayv1.Gateway,
	siteID int,
	proxyPort int,
	resourceID string,
	log logr.Logger,
) error {
	resources, err := r.PangolinClient.ListResources(ctx)
	if err != nil {
		return fmt.Errorf("failed to list resources for verification: %w", err)
	}

	var found map[string]interface{}
	for _, res := range resources {
		if fmt.Sprintf("%v", res["resourceId"]) == resourceID {
			found = res
			break
		}
	}

	if found == nil {
		// Resource gone. Check if a matching name already exists (concurrent recreation).
		expectedName := fmt.Sprintf("%s-%s", route.Namespace, route.Name)
		for _, res := range resources {
			if resName, _ := res["name"].(string); resName == expectedName {
				if newID := fmt.Sprintf("%v", res["resourceId"]); newID != "" && newID != "<nil>" {
					original := route.DeepCopy()
					if route.Labels == nil {
						route.Labels = make(map[string]string)
					}
					route.Labels[ResourceIDLabel] = newID
					if patchErr := r.Patch(ctx, route, client.MergeFrom(original)); patchErr != nil {
						return fmt.Errorf("failed to adopt existing resource %s: %w", newID, patchErr)
					}
					log.Info("Adopted existing resource with matching name", "resourceID", newID, "name", expectedName)
					r.Recorder.Eventf(route, corev1.EventTypeWarning, "DriftDetected",
						"Adopted existing Pangolin resource %s for %s", newID, expectedName)
					return nil
				}
			}
		}
		log.Info("Resource not found in Pangolin, recreating", "resourceID", resourceID)
		r.Recorder.Eventf(route, corev1.EventTypeWarning, "DriftDetected",
			"Pangolin resource %s not found, recreating", resourceID)
		return r.recreateTCPResource(ctx, route, gateway, siteID, proxyPort, resourceID, log)
	}

	// Resource exists — log name drift (informational)
	expectedName := fmt.Sprintf("%s-%s", route.Namespace, route.Name)
	if name, ok := found["name"].(string); ok && name != expectedName {
		log.Info("Resource name drift detected (cannot auto-correct)",
			"resourceID", resourceID, "expected", expectedName, "actual", name)
	}

	return nil
}

// recreateTCPResource creates a new Pangolin resource after the original was
// deleted, updates the route label with the new resource ID, and emits an Event.
func (r *TCPRouteReconciler) recreateTCPResource(
	ctx context.Context,
	route *gatewayv1alpha2.TCPRoute,
	gateway *gatewayv1.Gateway,
	siteID int,
	proxyPort int,
	oldResourceID string,
	log logr.Logger,
) error {
	newResourceID, err := r.createTCPPangolinResource(ctx, route, gateway, siteID, proxyPort, log)
	if err != nil {
		return fmt.Errorf("failed to recreate resource: %w", err)
	}
	original := route.DeepCopy()
	if route.Labels == nil {
		route.Labels = make(map[string]string)
	}
	route.Labels[ResourceIDLabel] = newResourceID
	if err := r.Patch(ctx, route, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("failed to patch route with new resource ID: %w", err)
	}
	log.Info("Recreated resource after deletion", "oldResourceID", oldResourceID, "newResourceID", newResourceID)
	r.Recorder.Eventf(route, corev1.EventTypeNormal, "ResourceRecreated",
		"Recreated Pangolin resource (old: %s, new: %s)", oldResourceID, newResourceID)
	return nil
}

// reconcileTCPTargets creates Pangolin targets for each backend in the TCPRoute
// rules. Existing matching targets are preserved; stale targets are removed.
func (r *TCPRouteReconciler) reconcileTCPTargets(
	ctx context.Context,
	route *gatewayv1alpha2.TCPRoute,
	resourceID string,
	siteID int,
	log logr.Logger,
) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	existingTargets, err := r.PangolinClient.ListTargetsRaw(ctx, resourceID)
	if err != nil {
		return fmt.Errorf("failed to list targets for resource %s: %w", resourceID, err)
	}

	type endpoint struct {
		ip   string
		port int
	}
	desired := map[endpoint]bool{}

	for _, rule := range route.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			// Skip zero-weight backends — they should not receive traffic.
			if backendRef.Weight != nil && *backendRef.Weight == 0 {
				log.V(1).Info("Skipping zero-weight backend", "backend", backendRef.Name)
				continue
			}
			if backendRef.Port == nil {
				log.Error(nil, "BackendRef missing required Port, skipping", "backend", string(backendRef.Name))
				continue
			}

			svcNS := route.Namespace
			if backendRef.Namespace != nil {
				svcNS = string(*backendRef.Namespace)
			}

			// ReferenceGrant check for cross-namespace Service references.
			if svcNS != route.Namespace {
				if err := r.checkReferenceGrant(ctx, route.Namespace, svcNS, backendRef.Name); err != nil {
					return fmt.Errorf("ReferenceGrant denied for %s/%s: %w", svcNS, backendRef.Name, err)
				}
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			svc := &corev1.Service{}
			if err := r.Get(ctx, types.NamespacedName{Name: string(backendRef.Name), Namespace: svcNS}, svc); err != nil {
				return fmt.Errorf("failed to get service %s/%s: %w", svcNS, backendRef.Name, err)
			}
			ip := svc.Spec.ClusterIP
			if ip == "" || ip == "None" {
				log.Info("Service has no ClusterIP, skipping", "service", string(backendRef.Name))
				continue
			}
			desired[endpoint{ip: ip, port: int(*backendRef.Port)}] = true
		}
	}

	// Determine which existing targets match a desired endpoint.
	matchedIDs := map[string]bool{}
	for _, t := range existingTargets {
		tIP, _ := t["ip"].(string)
		tPort, _ := t["port"].(float64)
		tSiteID, _ := t["siteId"].(float64)
		rawTargetID := t["targetId"]
		if rawTargetID == nil {
			continue
		}
		tID := fmt.Sprintf("%v", rawTargetID)
		ep := endpoint{ip: tIP, port: int(tPort)}
		if desired[ep] && int(tSiteID) == siteID {
			matchedIDs[tID] = true
		}
	}

	// Create missing desired targets.
	for ep := range desired {
		alreadyExists := false
		for _, t := range existingTargets {
			tIP, _ := t["ip"].(string)
			tPort, _ := t["port"].(float64)
			tSiteID, _ := t["siteId"].(float64)
			if tIP == ep.ip && int(tPort) == ep.port && int(tSiteID) == siteID {
				alreadyExists = true
				break
			}
		}
		if alreadyExists {
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		payload := map[string]any{
			"ip":      ep.ip,
			"port":    ep.port,
			"siteId":  siteID,
			"enabled": true,
		}
		if _, err := r.PangolinClient.CreateTargetRaw(ctx, resourceID, payload); err != nil {
			return fmt.Errorf("failed to create target %s:%d: %w", ep.ip, ep.port, err)
		}
		log.Info("Created TCP target", "ip", ep.ip, "port", ep.port, "resourceID", resourceID)
	}

	// Delete orphaned targets that no longer match a desired endpoint.
	for _, t := range existingTargets {
		rawTargetID := t["targetId"]
		if rawTargetID == nil {
			continue
		}
		tID := fmt.Sprintf("%v", rawTargetID)
		if matchedIDs[tID] {
			continue
		}
		if err := r.PangolinClient.DeleteTarget(ctx, tID); err != nil {
			return fmt.Errorf("failed to delete orphaned TCP target %s: %w", tID, err)
		}
		log.Info("Deleted orphaned TCP target", "targetId", tID)
	}
	return nil
}

// checkReferenceGrant verifies that a ReferenceGrant in toNamespace permits
// a TCPRoute in fromNamespace to reference the named Service.
func (r *TCPRouteReconciler) checkReferenceGrant(
	ctx context.Context,
	fromNamespace, toNamespace string,
	svcName gatewayv1.ObjectName,
) error {
	var grants gatewayv1beta1.ReferenceGrantList
	if err := r.List(ctx, &grants, client.InNamespace(toNamespace)); err != nil {
		return fmt.Errorf("failed to list ReferenceGrants in %s: %w", toNamespace, err)
	}
	for _, grant := range grants.Items {
		for _, from := range grant.Spec.From {
			if string(from.Group) == gatewayv1.GroupName &&
				string(from.Kind) == "TCPRoute" &&
				string(from.Namespace) == fromNamespace {
				for _, to := range grant.Spec.To {
					if (string(to.Group) == "" || string(to.Group) == "core") &&
						string(to.Kind) == "Service" {
						if to.Name == nil || *to.Name == svcName {
							return nil
						}
					}
				}
			}
		}
	}
	return fmt.Errorf("no ReferenceGrant allows TCPRoute in %s to reference Service %s/%s",
		fromNamespace, toNamespace, svcName)
}

// tcpParentRefKey produces a unique string key for a ParentReference to
// distinguish parent entries by (namespace, name, sectionName, port).
func tcpParentRefKey(pr gatewayv1.ParentReference) string {
	ns := ""
	if pr.Namespace != nil {
		ns = string(*pr.Namespace)
	}
	section := ""
	if pr.SectionName != nil {
		section = string(*pr.SectionName)
	}
	port := ""
	if pr.Port != nil {
		port = strconv.Itoa(int(*pr.Port))
	}
	return ns + "/" + string(pr.Name) + "/" + section + "/" + port
}

// updateTCPRouteStatus writes a RouteParentStatus condition to the TCPRoute,
// preserving LastTransitionTime when the status has not changed.
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
	parentKey := tcpParentRefKey(parentRef)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &gatewayv1alpha2.TCPRoute{}
		if err := r.Get(ctx, key, current); err != nil {
			return err
		}
		condStatus := metav1.ConditionFalse
		if accepted {
			condStatus = metav1.ConditionTrue
		}
		condition := metav1.Condition{
			Type:               string(gatewayv1.RouteConditionAccepted),
			Status:             condStatus,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: current.Generation,
			LastTransitionTime: metav1.Now(),
		}

		// Preserve LastTransitionTime if this parent+controller status hasn't changed.
		for _, ps := range current.Status.Parents {
			if tcpParentRefKey(ps.ParentRef) == parentKey &&
				string(ps.ControllerName) == ControllerName {
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
			ParentRef:      parentRef,
			ControllerName: gatewayv1.GatewayController(ControllerName),
			Conditions:     []metav1.Condition{condition},
		}
		found := false
		for i, ps := range current.Status.Parents {
			if tcpParentRefKey(ps.ParentRef) == parentKey &&
				string(ps.ControllerName) == ControllerName {
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
	// Allow updates that set DeletionTimestamp to pass even though they do not
	// change the Generation (so the finalizer cleanup loop is triggered).
	deletionTrigger := predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return false },
		DeleteFunc:  func(e event.DeleteEvent) bool { return false },
		GenericFunc: func(e event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return e.ObjectOld.GetDeletionTimestamp().IsZero() &&
				!e.ObjectNew.GetDeletionTimestamp().IsZero()
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("tcproute").
		For(&gatewayv1alpha2.TCPRoute{},
			builder.WithPredicates(predicate.Or(
				predicate.GenerationChangedPredicate{},
				deletionTrigger,
			)),
		).
		// Enqueue all TCPRoutes whose parent Gateway changed (e.g. site ID label added).
		Watches(&gatewayv1.Gateway{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				var routes gatewayv1alpha2.TCPRouteList
				if err := mgr.GetClient().List(ctx, &routes); err != nil {
					return nil
				}
				var reqs []reconcile.Request
				for _, route := range routes.Items {
					for _, ref := range route.Spec.ParentRefs {
						ns := route.Namespace
						if ref.Namespace != nil {
							ns = string(*ref.Namespace)
						}
						if string(ref.Name) == obj.GetName() && ns == obj.GetNamespace() {
							reqs = append(reqs, reconcile.Request{
								NamespacedName: types.NamespacedName{
									Name:      route.Name,
									Namespace: route.Namespace,
								},
							})
						}
					}
				}
				return reqs
			}),
		).
		// Enqueue all TCPRoutes referencing a Service whose ClusterIP changed.
		Watches(&corev1.Service{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				var routes gatewayv1alpha2.TCPRouteList
				if err := mgr.GetClient().List(ctx, &routes); err != nil {
					return nil
				}
				var reqs []reconcile.Request
				for _, route := range routes.Items {
					for _, rule := range route.Spec.Rules {
						for _, ref := range rule.BackendRefs {
							ns := route.Namespace
							if ref.Namespace != nil {
								ns = string(*ref.Namespace)
							}
							if string(ref.Name) == obj.GetName() && ns == obj.GetNamespace() {
								reqs = append(reqs, reconcile.Request{
									NamespacedName: types.NamespacedName{
										Name:      route.Name,
										Namespace: route.Namespace,
									},
								})
							}
						}
					}
				}
				return reqs
			}),
		).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.Config.MaxConcurrentReconciles,
			RateLimiter:             buildRateLimiter(r.Config),
		}).
		Complete(r)
}
