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

	route := &gatewayv1.TCPRoute{}
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
func (r *TCPRouteReconciler) handleTCPDelete(ctx context.Context, route *gatewayv1.TCPRoute, log logr.Logger) (ctrl.Result, error) {
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

// managedParent holds resolved information for a single parentRef managed by
// this controller.
type tcpManagedParent struct {
	parentRef gatewayv1.ParentReference
	gateway   *gatewayv1.Gateway
	proxyPort int
	siteID    int
}

// reconcileTCPRoute ensures a Pangolin resource and target exist for the route.
func (r *TCPRouteReconciler) reconcileTCPRoute(ctx context.Context, route *gatewayv1.TCPRoute, log logr.Logger) (ctrl.Result, error) {
	// Build the current parentRef key set once; used for status pruning throughout.
	currentParentKeys := make(map[string]bool, len(route.Spec.ParentRefs))
	for _, pr := range route.Spec.ParentRefs {
		currentParentKeys[tcpParentRefKey(pr)] = true
	}

	if len(route.Spec.ParentRefs) == 0 {
		log.Info("No parent gateway references found")
		// Clean up any previously created Pangolin resource and stale statuses.
		if resourceID := route.Labels[ResourceIDLabel]; resourceID != "" {
			if err := r.deleteTCPPangolinResource(ctx, resourceID, route, log); err != nil {
				return ctrl.Result{RequeueAfter: 30 * time.Second}, err
			}
		}
		r.pruneStaleTCPParentStatuses(ctx, route, currentParentKeys)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Collect all parent gateways managed by this controller and resolve each
	// to a proxyPort + siteID pair.
	var managed []tcpManagedParent
	anyGatewayNotReady := false

	for _, parentRef := range route.Spec.ParentRefs {
		// Skip parentRefs that explicitly target a non-Gateway resource type.
		// Default Group is gateway.networking.k8s.io and default Kind is Gateway;
		// if either is set to something else this controller cannot handle it.
		if parentRef.Group != nil && string(*parentRef.Group) != gatewayv1.GroupName {
			log.V(1).Info("Skipping parentRef with unsupported group", "group", string(*parentRef.Group))
			continue
		}
		if parentRef.Kind != nil && string(*parentRef.Kind) != "Gateway" {
			log.V(1).Info("Skipping parentRef with unsupported kind", "kind", string(*parentRef.Kind))
			continue
		}

		gatewayName := string(parentRef.Name)
		gatewayNamespace := route.Namespace
		if parentRef.Namespace != nil {
			gatewayNamespace = string(*parentRef.Namespace)
		}

		gateway := &gatewayv1.Gateway{}
		if err := r.Get(ctx, types.NamespacedName{Name: gatewayName, Namespace: gatewayNamespace}, gateway); err != nil {
			log.V(1).Info("Failed to get parent Gateway", "gateway", gatewayName, "error", err)
			r.updateTCPRouteStatus(ctx, route, parentRef, route.Generation, false, "ParentGatewayNotFound", "Parent gateway not found", "", "")
			continue
		}

		if string(gateway.Spec.GatewayClassName) != r.Config.GatewayClassName {
			log.V(1).Info("Parent gateway not managed by this controller, skipping",
				"gateway", gatewayName, "gatewayClass", gateway.Spec.GatewayClassName)
			continue
		}

		// Validate that the parentRef points to a TCP listener. Reject if the
		// section name is given but does not match, or the matched listener is
		// not TCP protocol.
		listenerPort, ok := resolveTCPListenerPort(gateway, parentRef)
		if !ok {
			sectionHint := ""
			if parentRef.SectionName != nil {
				sectionHint = string(*parentRef.SectionName)
			}
			log.Info("No matching TCP listener for parentRef", "gateway", gatewayName, "sectionName", sectionHint)
			r.updateTCPRouteStatus(ctx, route, parentRef, route.Generation, false, "NoMatchingListener",
				"No matching TCP listener found on parent gateway", "", "")
			continue
		}

		siteIDStr := gateway.Labels[SiteIDLabel]
		if siteIDStr == "" {
			log.Info("Gateway does not have site ID yet", "gateway", gatewayName)
			r.updateTCPRouteStatus(ctx, route, parentRef, route.Generation, false, "GatewayNotReady", "Parent gateway is not ready", "", "")
			anyGatewayNotReady = true
			continue
		}

		siteID, err := strconv.Atoi(siteIDStr)
		if err != nil {
			log.Error(err, "Invalid site ID label on Gateway", "siteIDStr", siteIDStr)
			r.updateTCPRouteStatus(ctx, route, parentRef, route.Generation, false, "InvalidSiteID",
				fmt.Sprintf("Invalid site ID: %s", siteIDStr), "", "")
			continue
		}

		managed = append(managed, tcpManagedParent{
			parentRef: parentRef,
			gateway:   gateway,
			proxyPort: listenerPort,
			siteID:    siteID,
		})
	}

	if len(managed) == 0 {
		// No valid parent remains — clean up any previously owned Pangolin resource
		// to stop forwarding traffic for a definitively detached route.
		if resourceID := route.Labels[ResourceIDLabel]; resourceID != "" {
			if err := r.deleteTCPPangolinResource(ctx, resourceID, route, log); err != nil {
				return ctrl.Result{RequeueAfter: 30 * time.Second}, err
			}
		}
		r.pruneStaleTCPParentStatuses(ctx, route, currentParentKeys)
		if anyGatewayNotReady {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		log.Info("No managed parent gateways found")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Add finalizer after confirming at least one gateway is ours.
	if !controllerutil.ContainsFinalizer(route, FinalizerName) {
		original := route.DeepCopy()
		controllerutil.AddFinalizer(route, FinalizerName)
		if err := r.Patch(ctx, route, client.MergeFrom(original)); err != nil {
			log.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
	}

	// Use the first managed parent as the primary for the Pangolin resource.
	// Additional parents are explicitly rejected (see status block below).
	primary := managed[0]
	siteID := primary.siteID
	proxyPort := primary.proxyPort
	gateway := primary.gateway
	siteIDStr := gateway.Labels[SiteIDLabel]

	resourceID := route.Labels[ResourceIDLabel]
	if resourceID == "" {
		// Adopt existing resource by name before creating a new one to prevent
		// duplicate resources when the label was lost.
		newID, err := r.adoptOrCreateTCPResource(ctx, route, gateway, siteID, proxyPort, log)
		if err != nil {
			log.Error(err, "Failed to create Pangolin resource")
			for _, p := range managed {
				r.updateTCPRouteStatus(ctx, route, p.parentRef, route.Generation, false, "ResourceCreationFailed", sanitizeEventMessage(err), "", "")
			}
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
		log.Info("Adopted or created Pangolin resource", "resourceID", resourceID)
		r.Recorder.Eventf(route, corev1.EventTypeNormal, "ResourceCreated", "Created Pangolin resource %s", resourceID)
	} else {
		if err := r.verifyOrRecreateTCPResource(ctx, route, gateway, siteID, proxyPort, resourceID, log); err != nil {
			log.Error(err, "Failed to verify/recreate resource")
			for _, p := range managed {
				r.updateTCPRouteStatus(ctx, route, p.parentRef, route.Generation, false, "ResourceVerificationFailed", sanitizeEventMessage(err), "", "")
			}
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}
		if updated := route.Labels[ResourceIDLabel]; updated != "" {
			resourceID = updated
		}
	}

	resolveReason, resolveMsg, err := r.reconcileTCPTargets(ctx, route, resourceID, siteID, log)
	if err != nil {
		if isSiteGoneError(err) {
			log.Info("Site deleted from Pangolin, clearing gateway site-id to trigger immediate recreation", "siteID", siteIDStr)
			r.Recorder.Eventf(route, corev1.EventTypeWarning, "SiteGone", "Pangolin site %s was deleted; triggering gateway reconcile", siteIDStr)
			original := gateway.DeepCopy()
			delete(gateway.Labels, SiteIDLabel)
			if patchErr := r.Patch(ctx, gateway, client.MergeFrom(original)); patchErr != nil {
				log.Error(patchErr, "Failed to clear gateway site-id label")
			}
			for _, p := range managed {
				r.updateTCPRouteStatus(ctx, route, p.parentRef, route.Generation, false, "SiteGone", "Parent gateway site was deleted; waiting for recreation", "", "")
			}
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		log.Error(err, "Failed to reconcile TCP targets")
		for _, p := range managed {
			r.updateTCPRouteStatus(ctx, route, p.parentRef, route.Generation, false, "TargetError", sanitizeEventMessage(err), "", "")
		}
		r.Recorder.Eventf(route, corev1.EventTypeWarning, "TargetError", "Failed to reconcile targets: %s", sanitizeEventMessage(err))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Mark the first (primary) parent as Accepted; explicitly reject additional
	// parents because only one site/proxyPort combination is programmed per route.
	for i, p := range managed {
		if i == 0 {
			r.updateTCPRouteStatus(ctx, route, p.parentRef, route.Generation, true, "Accepted",
				"TCPRoute is configured in Pangolin", resolveReason, resolveMsg)
		} else {
			r.updateTCPRouteStatus(ctx, route, p.parentRef, route.Generation, false,
				"MultipleParentsNotSupported",
				"TCPRoute supports only one active parent; this parent is not programmed", "", "")
		}
	}
	r.pruneStaleTCPParentStatuses(ctx, route, currentParentKeys)
	r.Recorder.Eventf(route, corev1.EventTypeNormal, "Accepted", "TCPRoute configured in Pangolin")
	log.Info("Successfully reconciled TCPRoute", "resourceID", resourceID)
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// resolveTCPListenerPort returns the listener port and true when the parentRef
// maps to a TCP-protocol listener on the gateway. Returns (0, false) when:
//   - sectionName is specified but no listener with that name exists
//   - the matched listener's protocol is not TCPProtocolType
//   - no TCP listener exists at all (when sectionName is absent)
func resolveTCPListenerPort(gateway *gatewayv1.Gateway, parentRef gatewayv1.ParentReference) (int, bool) {
	if parentRef.SectionName != nil {
		for _, listener := range gateway.Spec.Listeners {
			if listener.Name == *parentRef.SectionName {
				if listener.Protocol != gatewayv1.TCPProtocolType {
					return 0, false
				}
				return int(listener.Port), true
			}
		}
		return 0, false // named section not found
	}
	// No sectionName: use the first TCP listener.
	for _, listener := range gateway.Spec.Listeners {
		if listener.Protocol == gatewayv1.TCPProtocolType {
			return int(listener.Port), true
		}
	}
	return 0, false
}

// deleteTCPPangolinResource removes a Pangolin resource by ID and clears the
// route's ResourceIDLabel. 404 errors are treated as already-deleted.
func (r *TCPRouteReconciler) deleteTCPPangolinResource(ctx context.Context, resourceID string, route *gatewayv1.TCPRoute, log logr.Logger) error {
	if err := r.PangolinClient.DeleteResource(ctx, resourceID); err != nil {
		if apiErr, ok := pangolin.AsAPIError(err); ok && apiErr.StatusCode == 404 {
			log.Info("Resource already deleted (404)", "resourceID", resourceID)
		} else {
			return fmt.Errorf("failed to delete Pangolin resource %s: %w", resourceID, err)
		}
	}
	original := route.DeepCopy()
	delete(route.Labels, ResourceIDLabel)
	if err := r.Patch(ctx, route, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("failed to clear resource ID label: %w", err)
	}
	log.Info("Deleted Pangolin resource and cleared label", "resourceID", resourceID)
	return nil
}

// adoptOrCreateTCPResource looks for an existing Pangolin resource by the
// expected name and adopts it if found; otherwise creates a new one.
// This prevents duplicate resources when the ResourceIDLabel was lost.
func (r *TCPRouteReconciler) adoptOrCreateTCPResource(
	ctx context.Context,
	route *gatewayv1.TCPRoute,
	gateway *gatewayv1.Gateway,
	siteID int,
	proxyPort int,
	log logr.Logger,
) (string, error) {
	// Use a TCPRoute-specific prefix to avoid collision with GRPCRoute and other
	// controllers that also derive names from namespace/name.
	expectedName := fmt.Sprintf("tcp-%s-%s", route.Namespace, route.Name)

	resources, err := r.PangolinClient.ListResources(ctx)
	if err != nil {
		log.V(1).Info("Could not list resources for adopt check, proceeding to create", "error", err)
	} else {
		for _, res := range resources {
			if resName, _ := res["name"].(string); resName == expectedName {
				if id := fmt.Sprintf("%v", res["resourceId"]); id != "" && id != "<nil>" {
					log.Info("Adopted existing Pangolin resource by name", "resourceID", id, "name", expectedName)
					r.Recorder.Eventf(route, corev1.EventTypeNormal, "ResourceAdopted",
						"Adopted existing Pangolin resource %s", id)
					return id, nil
				}
			}
		}
	}

	return r.createTCPPangolinResource(ctx, route, proxyPort, log)
}

// createTCPPangolinResource creates a new Pangolin resource for this TCPRoute.
// TCPRoutes use "tcp" mode, indicating a raw TCP passthrough tunnel.
// The siteId field is intentionally omitted from the resource creation payload:
// Pangolin 1.25.3's strict schema does not accept it at resource creation time;
// site association is established through targets instead.
func (r *TCPRouteReconciler) createTCPPangolinResource(
	ctx context.Context,
	route *gatewayv1.TCPRoute,
	proxyPort int,
	log logr.Logger,
) (string, error) {
	// Use a TCPRoute-specific prefix to avoid collision with GRPCRoute resources.
	resourceName := fmt.Sprintf("tcp-%s-%s", route.Namespace, route.Name)

	payload := map[string]any{
		"name": resourceName,
		"mode": "tcp",
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
	route *gatewayv1.TCPRoute,
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

	// Use the TCPRoute-specific prefix to match the name used at creation time.
	expectedName := fmt.Sprintf("tcp-%s-%s", route.Namespace, route.Name)

	if found == nil {
		// Resource gone. Check if a matching name already exists (concurrent recreation).
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

	// Resource exists — log name drift (informational).
	if name, ok := found["name"].(string); ok && name != expectedName {
		log.Info("Resource name drift detected (cannot auto-correct)",
			"resourceID", resourceID, "expected", expectedName, "actual", name)
	}

	// Log proxyPort drift. The Pangolin Integration API does not support PATCH,
	// so automatic correction is not possible; manual intervention is required.
	if proxyPort > 0 {
		if existingPort, ok := found["proxyPort"].(float64); ok && int(existingPort) != proxyPort {
			log.Info("proxyPort drift detected (Integration API does not support PATCH; manual correction needed)",
				"resourceID", resourceID, "expected", proxyPort, "actual", int(existingPort))
		}
	}

	return nil
}

// recreateTCPResource creates a new Pangolin resource after the original was
// deleted, updates the route label with the new resource ID, and emits an Event.
func (r *TCPRouteReconciler) recreateTCPResource(
	ctx context.Context,
	route *gatewayv1.TCPRoute,
	gateway *gatewayv1.Gateway,
	siteID int,
	proxyPort int,
	oldResourceID string,
	log logr.Logger,
) error {
	newResourceID, err := r.createTCPPangolinResource(ctx, route, proxyPort, log)
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

// tcpBackendWeightsUnequal returns true when the rule has multiple backends
// with non-zero, non-equal weights. A single backend or nil/equal weights are fine.
func tcpBackendWeightsUnequal(refs []gatewayv1.BackendRef) bool {
	if len(refs) <= 1 {
		return false
	}
	var reference *int32
	for i := range refs {
		w := refs[i].Weight
		if w == nil || *w == 0 {
			continue
		}
		if reference == nil {
			reference = w
		} else if *w != *reference {
			return true
		}
	}
	return false
}

// reconcileTCPTargets creates Pangolin targets for each backend in the TCPRoute
// rules. Existing matching targets are preserved; stale targets are removed.
// Returns (resolveReason, resolveMsg, err):
//   - resolveReason/resolveMsg: non-empty when backend references could not be
//     fully resolved (e.g. denied ReferenceGrant, missing Service); used to set
//     the RouteConditionResolvedRefs status condition.
//   - err: a retriable API or Kubernetes error.
func (r *TCPRouteReconciler) reconcileTCPTargets(
	ctx context.Context,
	route *gatewayv1.TCPRoute,
	resourceID string,
	siteID int,
	log logr.Logger,
) (resolveReason, resolveMsg string, err error) {
	select {
	case <-ctx.Done():
		return "", "", ctx.Err()
	default:
	}

	existingTargets, err := r.PangolinClient.ListTargetsRaw(ctx, resourceID)
	if err != nil {
		return "", "", fmt.Errorf("failed to list targets for resource %s: %w", resourceID, err)
	}

	type endpoint struct {
		ip   string
		port int
	}
	desired := map[endpoint]bool{}

	setResolveErr := func(reason, msg string) {
		if resolveReason == "" {
			resolveReason = reason
			resolveMsg = msg
		}
	}

	for _, rule := range route.Spec.Rules {
		// Reject rules where backends specify unequal non-zero weights; Pangolin
		// TCP mode does not support weighted traffic distribution.
		if tcpBackendWeightsUnequal(rule.BackendRefs) {
			setResolveErr("UnsupportedValue",
				"unequal backend weights are not supported for TCP mode; all backends must have equal weights")
			continue
		}

		for _, backendRef := range rule.BackendRefs {
			// Skip zero-weight backends — they should not receive traffic.
			if backendRef.Weight != nil && *backendRef.Weight == 0 {
				log.V(1).Info("Skipping zero-weight backend", "backend", backendRef.Name)
				continue
			}

			// Only core (group="") Services are supported for TCP routing.
			if backendRef.Group != nil && string(*backendRef.Group) != "" {
				log.Info("Unsupported backend group, skipping", "group", string(*backendRef.Group), "backend", backendRef.Name)
				setResolveErr(string(gatewayv1.RouteReasonInvalidKind),
					fmt.Sprintf("unsupported backend group %q for backend %s", string(*backendRef.Group), backendRef.Name))
				continue
			}
			if backendRef.Kind != nil && string(*backendRef.Kind) != "Service" {
				log.Info("Unsupported backend kind, skipping", "kind", string(*backendRef.Kind), "backend", backendRef.Name)
				setResolveErr(string(gatewayv1.RouteReasonInvalidKind),
					fmt.Sprintf("unsupported backend kind %q for backend %s", string(*backendRef.Kind), backendRef.Name))
				continue
			}

			if backendRef.Port == nil {
				log.Error(nil, "BackendRef missing required Port, skipping", "backend", string(backendRef.Name))
				setResolveErr(string(gatewayv1.RouteReasonBackendNotFound),
					fmt.Sprintf("backend %s is missing required Port", backendRef.Name))
				continue
			}

			svcNS := route.Namespace
			if backendRef.Namespace != nil {
				svcNS = string(*backendRef.Namespace)
			}

			// ReferenceGrant check for cross-namespace Service references.
			// On explicit denial (allowed=false, err=nil) the backend is skipped
			// and orphan cleanup will remove any previously-created target.
			if svcNS != route.Namespace {
				allowed, refErr := r.checkReferenceGrant(ctx, route.Namespace, svcNS, backendRef.Name)
				if refErr != nil {
					return "", "", fmt.Errorf("failed to check ReferenceGrant for %s/%s: %w", svcNS, backendRef.Name, refErr)
				}
				if !allowed {
					log.Info("ReferenceGrant denied, skipping backend", "namespace", svcNS, "service", backendRef.Name)
					setResolveErr(string(gatewayv1.RouteReasonRefNotPermitted),
						fmt.Sprintf("ReferenceGrant does not permit reference from %s to %s/%s",
							route.Namespace, svcNS, backendRef.Name))
					continue
				}
			}

			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			default:
			}

			svc := &corev1.Service{}
			if svcErr := r.Get(ctx, types.NamespacedName{Name: string(backendRef.Name), Namespace: svcNS}, svc); svcErr != nil {
				if errors.IsNotFound(svcErr) {
					// Service deleted — skip and let orphan cleanup remove the stale target.
					log.Info("Backend Service not found, skipping", "service", backendRef.Name, "namespace", svcNS)
					setResolveErr(string(gatewayv1.RouteReasonBackendNotFound),
						fmt.Sprintf("service %s/%s not found", svcNS, backendRef.Name))
					continue
				}
				return "", "", fmt.Errorf("failed to get service %s/%s: %w", svcNS, backendRef.Name, svcErr)
			}
			ip := svc.Spec.ClusterIP
			if ip == "" || ip == "None" {
				log.Info("Service has no ClusterIP, skipping", "service", string(backendRef.Name))
				setResolveErr(string(gatewayv1.RouteReasonBackendNotFound),
					fmt.Sprintf("service %s/%s has no ClusterIP", svcNS, backendRef.Name))
				continue
			}

			// Verify the service exposes a matching TCP port before creating a target.
			portNum := int(*backendRef.Port)
			hasPort := false
			for _, sp := range svc.Spec.Ports {
				if int(sp.Port) == portNum && (sp.Protocol == "" || sp.Protocol == corev1.ProtocolTCP) {
					hasPort = true
					break
				}
			}
			if !hasPort {
				log.Info("Service has no matching TCP port, skipping", "service", backendRef.Name, "port", portNum)
				setResolveErr(string(gatewayv1.RouteReasonBackendNotFound),
					fmt.Sprintf("service %s/%s has no TCP port %d", svcNS, backendRef.Name, portNum))
				continue
			}

			desired[endpoint{ip: ip, port: portNum}] = true
		}
	}

	// Determine which existing targets match a desired endpoint. A target must
	// be enabled to count as matched; disabled targets will be deleted and
	// recreated so they are reliably in service.
	matchedIDs := map[string]bool{}
	for _, t := range existingTargets {
		tIP, _ := t["ip"].(string)
		tPort, _ := t["port"].(float64)
		tSiteID, _ := t["siteId"].(float64)
		tEnabled, _ := t["enabled"].(bool)
		rawTargetID := t["targetId"]
		if rawTargetID == nil {
			continue
		}
		tID := fmt.Sprintf("%v", rawTargetID)
		ep := endpoint{ip: tIP, port: int(tPort)}
		if desired[ep] && int(tSiteID) == siteID && tEnabled {
			matchedIDs[tID] = true
		}
	}

	// Create missing desired targets. A target only qualifies as existing if it
	// is enabled; disabled targets will be deleted and recreated below.
	for ep := range desired {
		alreadyExists := false
		for _, t := range existingTargets {
			tIP, _ := t["ip"].(string)
			tPort, _ := t["port"].(float64)
			tSiteID, _ := t["siteId"].(float64)
			tEnabled, _ := t["enabled"].(bool)
			if tIP == ep.ip && int(tPort) == ep.port && int(tSiteID) == siteID && tEnabled {
				alreadyExists = true
				break
			}
		}
		if alreadyExists {
			continue
		}

		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		default:
		}

		payload := map[string]any{
			"ip":      ep.ip,
			"port":    ep.port,
			"siteId":  siteID,
			"enabled": true,
		}
		if _, createErr := r.PangolinClient.CreateTargetRaw(ctx, resourceID, payload); createErr != nil {
			return "", "", fmt.Errorf("failed to create target %s:%d: %w", ep.ip, ep.port, createErr)
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
		if deleteErr := r.PangolinClient.DeleteTarget(ctx, tID); deleteErr != nil {
			return "", "", fmt.Errorf("failed to delete orphaned TCP target %s: %w", tID, deleteErr)
		}
		log.Info("Deleted orphaned TCP target", "targetId", tID)
	}
	return resolveReason, resolveMsg, nil
}

// checkReferenceGrant verifies that a ReferenceGrant in toNamespace permits
// a TCPRoute in fromNamespace to reference the named Service.
// Returns (true, nil) when permitted, (false, nil) when explicitly denied
// (no matching grant), and (false, err) on a lookup failure.
func (r *TCPRouteReconciler) checkReferenceGrant(
	ctx context.Context,
	fromNamespace, toNamespace string,
	svcName gatewayv1.ObjectName,
) (bool, error) {
	var grants gatewayv1beta1.ReferenceGrantList
	if err := r.List(ctx, &grants, client.InNamespace(toNamespace)); err != nil {
		return false, fmt.Errorf("failed to list ReferenceGrants in %s: %w", toNamespace, err)
	}
	for _, grant := range grants.Items {
		for _, from := range grant.Spec.From {
			if string(from.Group) == gatewayv1.GroupName &&
				string(from.Kind) == "TCPRoute" &&
				string(from.Namespace) == fromNamespace {
				for _, to := range grant.Spec.To {
					// Core API group is represented as empty string only, not "core".
					if string(to.Group) == "" && string(to.Kind) == "Service" {
						if to.Name == nil || *to.Name == svcName {
							return true, nil
						}
					}
				}
			}
		}
	}
	return false, nil
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

// updateTCPRouteStatus writes RouteParentStatus conditions to the TCPRoute for
// the given parentRef, preserving LastTransitionTime when unchanged.
// observedGeneration is taken from the reconciled route snapshot, not the
// freshly-fetched copy, to avoid reporting a generation we have not yet processed.
// resolveReason/resolveMsg are used to set the ResolvedRefs condition; empty
// strings indicate all backend references resolved successfully.
func (r *TCPRouteReconciler) updateTCPRouteStatus(
	ctx context.Context,
	route *gatewayv1.TCPRoute,
	parentRef gatewayv1.ParentReference,
	observedGeneration int64,
	accepted bool,
	reason, message string,
	resolveReason, resolveMsg string,
) {
	key := client.ObjectKeyFromObject(route)
	parentKey := tcpParentRefKey(parentRef)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &gatewayv1.TCPRoute{}
		if err := r.Get(ctx, key, current); err != nil {
			return err
		}
		condStatus := metav1.ConditionFalse
		if accepted {
			condStatus = metav1.ConditionTrue
		}
		acceptedCond := metav1.Condition{
			Type:               string(gatewayv1.RouteConditionAccepted),
			Status:             condStatus,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: observedGeneration,
			LastTransitionTime: metav1.Now(),
		}

		resolveStatus := metav1.ConditionTrue
		resolveReasonVal := string(gatewayv1.RouteReasonResolvedRefs)
		resolveMsgVal := "All backend references are resolved"
		if resolveReason != "" {
			resolveStatus = metav1.ConditionFalse
			resolveReasonVal = resolveReason
			resolveMsgVal = resolveMsg
		}
		resolvedRefsCond := metav1.Condition{
			Type:               string(gatewayv1.RouteConditionResolvedRefs),
			Status:             resolveStatus,
			Reason:             resolveReasonVal,
			Message:            resolveMsgVal,
			ObservedGeneration: observedGeneration,
			LastTransitionTime: metav1.Now(),
		}

		// Preserve LastTransitionTime if the condition status has not changed.
		for _, ps := range current.Status.Parents {
			if tcpParentRefKey(ps.ParentRef) == parentKey &&
				string(ps.ControllerName) == ControllerName {
				for _, existing := range ps.Conditions {
					switch existing.Type {
					case acceptedCond.Type:
						if existing.Status == acceptedCond.Status {
							acceptedCond.LastTransitionTime = existing.LastTransitionTime
						}
					case resolvedRefsCond.Type:
						if existing.Status == resolvedRefsCond.Status {
							resolvedRefsCond.LastTransitionTime = existing.LastTransitionTime
						}
					}
				}
				break
			}
		}

		newParent := gatewayv1.RouteParentStatus{
			ParentRef:      parentRef,
			ControllerName: gatewayv1.GatewayController(ControllerName),
			Conditions:     []metav1.Condition{acceptedCond, resolvedRefsCond},
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

// pruneStaleTCPParentStatuses removes controller-owned RouteParentStatus entries
// for parentRefs that are no longer present in currentParentKeys. This prevents
// unbounded growth of the status slice and avoids stale Accepted entries after
// a parentRef is removed from the spec.
func (r *TCPRouteReconciler) pruneStaleTCPParentStatuses(
	ctx context.Context,
	route *gatewayv1.TCPRoute,
	currentParentKeys map[string]bool,
) {
	key := client.ObjectKeyFromObject(route)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &gatewayv1.TCPRoute{}
		if err := r.Get(ctx, key, current); err != nil {
			return err
		}

		var pruned []gatewayv1.RouteParentStatus
		changed := false
		for _, ps := range current.Status.Parents {
			if string(ps.ControllerName) != ControllerName {
				// Preserve entries owned by other controllers.
				pruned = append(pruned, ps)
				continue
			}
			if currentParentKeys[tcpParentRefKey(ps.ParentRef)] {
				pruned = append(pruned, ps)
			} else {
				changed = true
			}
		}

		if !changed {
			return nil
		}

		current.Status.Parents = pruned
		return r.Status().Update(ctx, current)
	})
	if err != nil {
		r.Log.Error(err, "Failed to prune stale TCPRoute parent statuses")
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
		For(&gatewayv1.TCPRoute{},
			builder.WithPredicates(predicate.Or(
				predicate.GenerationChangedPredicate{},
				deletionTrigger,
			)),
		).
		// Enqueue all TCPRoutes whose parent Gateway changed (e.g. site ID label added).
		Watches(&gatewayv1.Gateway{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				var routes gatewayv1.TCPRouteList
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
				var routes gatewayv1.TCPRouteList
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
