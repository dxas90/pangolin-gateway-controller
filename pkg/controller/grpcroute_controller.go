package controller

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/dxas90/pangolin-gateway-controller/pkg/config"
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
)

// sanitizeGRPCEventMessage truncates error messages for Kubernetes events to avoid
// leaking large API response bodies into event records.
func sanitizeGRPCEventMessage(err error) string {
	msg := err.Error()
	if len(msg) > 200 {
		return msg[:200] + "..."
	}
	return msg
}

// numericToStringGRPC converts numeric interface values to string for comparison
func numericToStringGRPC(v interface{}) string {
	switch n := v.(type) {
	case float64:
		return fmt.Sprintf("%.0f", n)
	case int:
		return strconv.Itoa(n)
	case int64:
		return strconv.FormatInt(n, 10)
	case string:
		return n
	default:
		return fmt.Sprintf("%v", v)
	}
}

// GRPCRouteReconciler reconciles GRPCRoute resources for TCP/UDP services
type GRPCRouteReconciler struct {
	client.Client
	Log            logr.Logger
	Scheme         *runtime.Scheme
	PangolinClient pangolin.ClientInterface
	Config         *config.ControllerConfig
	Recorder       record.EventRecorder
}

// Reconcile implements the reconciliation logic for GRPCRoute resources
func (r *GRPCRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("grpcroute", req.NamespacedName)
	log.Info("Reconciling GRPCRoute")

	ctx, cancel := context.WithTimeout(ctx, ReconcileTimeout)
	defer cancel()

	// Fetch the GRPCRoute instance
	route := &gatewayv1.GRPCRoute{}
	if err := r.Get(ctx, req.NamespacedName, route); err != nil {
		if errors.IsNotFound(err) {
			log.Info("GRPCRoute resource not found, likely deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get GRPCRoute")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !route.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.handleDelete(ctx, route, log)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(route, FinalizerName) {
		original := route.DeepCopy()
		controllerutil.AddFinalizer(route, FinalizerName)
		if err := r.Patch(ctx, route, client.MergeFrom(original)); err != nil {
			log.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
	}

	// Reconcile the GRPCRoute
	return r.reconcileGRPCRoute(ctx, route, log)
}

// handleDelete handles the deletion of a GRPCRoute resource
func (r *GRPCRouteReconciler) handleDelete(ctx context.Context, route *gatewayv1.GRPCRoute, log logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(route, FinalizerName) {
		return ctrl.Result{}, nil
	}

	log.Info("Deleting GRPCRoute from Pangolin")

	// Delete the entire resource from Pangolin (this also deletes all targets)
	resourceID := route.Labels[ResourceIDLabel]
	if resourceID != "" {
		log.Info("Deleting Pangolin resource", "resourceID", resourceID)
		if err := r.PangolinClient.DeleteResource(ctx, resourceID); err != nil {
			log.Error(err, "Failed to delete resource from Pangolin", "resourceID", resourceID)
			// Continue with finalizer removal even if deletion fails
			// (resource might already be deleted)
		} else {
			log.Info("Successfully deleted Pangolin resource", "resourceID", resourceID)
		}
	} else {
		log.Info("No resource ID found in labels, skipping Pangolin cleanup")
	}

	// Remove finalizer
	original := route.DeepCopy()
	controllerutil.RemoveFinalizer(route, FinalizerName)
	if err := r.Patch(ctx, route, client.MergeFrom(original)); err != nil {
		log.Error(err, "Failed to remove finalizer")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// reconcileGRPCRoute reconciles the GRPCRoute with Pangolin
func (r *GRPCRouteReconciler) reconcileGRPCRoute(ctx context.Context, route *gatewayv1.GRPCRoute, log logr.Logger) (ctrl.Result, error) {
	// Get the parent Gateway
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

	// Fetch the Gateway
	gateway := &gatewayv1.Gateway{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      gatewayName,
		Namespace: gatewayNamespace,
	}, gateway); err != nil {
		log.Error(err, "Failed to get parent Gateway")
		r.updateRouteStatus(ctx, route, false, "ParentGatewayNotFound", "Parent gateway not found")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Get the site ID from the Gateway
	siteIDStr := gateway.Labels[SiteIDLabel]
	if siteIDStr == "" {
		log.Info("Gateway does not have site ID yet")
		r.updateRouteStatus(ctx, route, false, "GatewayNotReady", "Parent gateway is not ready")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Get or create resource ID
	resourceID := route.Labels[ResourceIDLabel]
	if resourceID == "" {
		// Create new Pangolin resource for this GRPCRoute
		newResourceID, err := r.createPangolinResource(ctx, route, gateway, log)
		if err != nil {
			log.Error(err, "Failed to create Pangolin resource")
			r.updateRouteStatus(ctx, route, false, "ResourceCreationFailed", err.Error())
			r.Recorder.Eventf(route, corev1.EventTypeWarning, "ResourceCreationFailed", "Failed to create Pangolin resource: %s", sanitizeGRPCEventMessage(err))
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}
		resourceID = newResourceID

		// Update route labels with resource ID
		original := route.DeepCopy()
		if route.Labels == nil {
			route.Labels = make(map[string]string)
		}
		route.Labels[ResourceIDLabel] = resourceID
		if err := r.Patch(ctx, route, client.MergeFrom(original)); err != nil {
			log.Error(err, "Failed to update GRPCRoute with resource ID")
			return ctrl.Result{}, err
		}
		log.Info("Created Pangolin resource", "resourceID", resourceID)
		r.Recorder.Eventf(route, corev1.EventTypeNormal, "ResourceCreated", "Created Pangolin resource %s", resourceID)
	} else {
		// Verify resource still exists in Pangolin, recreate if deleted
		if err := r.verifyOrRecreateResource(ctx, route, gateway, resourceID, log); err != nil {
			log.Error(err, "Failed to verify/recreate resource")
			r.updateRouteStatus(ctx, route, false, "ResourceVerificationFailed", err.Error())
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}
	}

	// Create or update targets for backends
	if err := r.reconcileTargets(ctx, route, resourceID, siteIDStr, gateway, log); err != nil {
		log.Error(err, "Failed to reconcile targets")
		r.updateRouteStatus(ctx, route, false, "TargetError", err.Error())
		r.Recorder.Eventf(route, corev1.EventTypeWarning, "TargetError", "Failed to reconcile targets: %s", sanitizeGRPCEventMessage(err))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Update GRPCRoute status
	r.updateRouteStatus(ctx, route, true, "Accepted", "GRPCRoute is configured in Pangolin")
	r.Recorder.Eventf(route, corev1.EventTypeNormal, "Accepted", "GRPCRoute configured in Pangolin")

	log.Info("Successfully reconciled GRPCRoute", "resourceID", resourceID)
	// Requeue after 5 minutes to periodically verify resource still exists
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// verifyOrRecreateResource checks if the resource exists in Pangolin and recreates if deleted
func (r *GRPCRouteReconciler) verifyOrRecreateResource(ctx context.Context, route *gatewayv1.GRPCRoute, gateway *gatewayv1.Gateway, resourceID string, log logr.Logger) error {
	// Try to get existing targets to verify resource exists
	_, err := r.PangolinClient.ListTargetsRaw(ctx, resourceID)
	if err != nil {
		// Resource likely doesn't exist, recreate it
		log.Info("Resource not found in Pangolin, recreating", "resourceID", resourceID)
		newResourceID, createErr := r.createPangolinResource(ctx, route, gateway, log)
		if createErr != nil {
			return fmt.Errorf("failed to recreate resource: %w", createErr)
		}
		// Update route labels with new resource ID using Patch to avoid overwriting
		original := route.DeepCopy()
		if route.Labels == nil {
			route.Labels = make(map[string]string)
		}
		route.Labels[ResourceIDLabel] = newResourceID
		if err := r.Patch(ctx, route, client.MergeFrom(original)); err != nil {
			return fmt.Errorf("failed to patch route with new resource ID: %w", err)
		}
		log.Info("Recreated resource after deletion", "oldResourceID", resourceID, "newResourceID", newResourceID)
	}
	return nil
}

// reconcileTargets creates or updates backend targets in Pangolin
func (r *GRPCRouteReconciler) reconcileTargets(ctx context.Context, route *gatewayv1.GRPCRoute, resourceID, siteID string, gateway *gatewayv1.Gateway, log logr.Logger) error {
	// Check for context cancellation before starting expensive operations
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Collect all unique backends from all rules
	backendMap := make(map[string]gatewayv1.BackendRef)

	for _, rule := range route.Spec.Rules {
		for _, backendRef := range rule.BackendRefs {
			key := fmt.Sprintf("%s:%d", string(backendRef.Name), *backendRef.Port)
			backendMap[key] = backendRef.BackendRef
		}
	}

	if len(backendMap) == 0 {
		return fmt.Errorf("no backend services found in GRPCRoute")
	}

	// Get existing targets from Pangolin
	existingTargets, err := r.PangolinClient.ListTargetsRaw(ctx, resourceID)
	if err != nil {
		log.Error(err, "Failed to list existing targets, will attempt to create anyway")
		existingTargets = []map[string]interface{}{} // Continue with empty list
	}

	// Parse siteID once
	siteIDInt, err := strconv.Atoi(siteID)
	if err != nil {
		return fmt.Errorf("invalid site ID %s: %w", siteID, err)
	}

	// Get Service ClusterIP for each backend
	for key, backendRef := range backendMap {
		// Check for context cancellation in loop
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		serviceName := string(backendRef.Name)
		serviceNamespace := route.Namespace
		if backendRef.Namespace != nil {
			serviceNamespace = string(*backendRef.Namespace)
		}

		// Get the Service to find its ClusterIP
		svc := &corev1.Service{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      serviceName,
			Namespace: serviceNamespace,
		}, svc); err != nil {
			return fmt.Errorf("failed to get service %s: %w", serviceName, err)
		}

		clusterIP := svc.Spec.ClusterIP
		if clusterIP == "" || clusterIP == "None" {
			return fmt.Errorf("service %s has no ClusterIP", serviceName)
		}

		port := int(*backendRef.Port)

		// Check if target already exists with same ip:port:siteId
		targetExists := false
		for _, target := range existingTargets {
			if fmt.Sprintf("%v", target["ip"]) == clusterIP &&
				numericToStringGRPC(target["port"]) == strconv.Itoa(port) &&
				numericToStringGRPC(target["siteId"]) == strconv.Itoa(siteIDInt) {
				targetExists = true
				log.Info("Target already exists, skipping", "ip", clusterIP, "port", port, "targetId", target["targetId"])
				break
			}
		}

		if targetExists {
			continue
		}

		// Check for context cancellation before create operation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Create target via Integration API: PUT /resource/{resourceId}/target
		targetData := map[string]interface{}{
			"siteId":  siteIDInt,
			"ip":      clusterIP,
			"port":    port,
			"enabled": true,
		}

		createdTarget, err := r.PangolinClient.CreateTargetRaw(ctx, resourceID, targetData)
		if err != nil {
			return fmt.Errorf("failed to create target %s: %w", key, err)
		}

		targetID := fmt.Sprintf("%v", createdTarget["targetId"])
		log.Info("Created target", "targetID", targetID, "ip", clusterIP, "port", port, "service", serviceName)
		r.Recorder.Eventf(route, corev1.EventTypeNormal, "TargetCreated",
			"Created target %s (%s:%d)", targetID, clusterIP, port)
	}

	return nil
}

// createPangolinResource creates a new resource in Pangolin for the GRPCRoute (TCP/UDP)
func (r *GRPCRouteReconciler) createPangolinResource(ctx context.Context, route *gatewayv1.GRPCRoute, gateway *gatewayv1.Gateway, log logr.Logger) (string, error) {
	// Get organization domains
	domains, err := r.PangolinClient.ListDomains(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list organization domains: %w", err)
	}

	// Get the first hostname from the GRPCRoute
	subdomain := fmt.Sprintf("%s-%s", route.Namespace, route.Name)
	domainID := "domain1" // Default fallback
	protocol := "tcp"     // Default to TCP for gRPC

	if len(route.Spec.Hostnames) > 0 {
		hostname := string(route.Spec.Hostnames[0])

		// Use the shared extractSubdomainFromDomains helper with baseDomain field
		if sub, id, err := extractSubdomainFromDomains(hostname, domains); err == nil {
			subdomain = sub
			domainID = id
			log.Info("Matched hostname to domain", "hostname", hostname, "subdomain", subdomain, "domainID", domainID)
		}
	}

	// Check annotations for protocol override (tcp or udp)
	if proto, ok := route.Annotations["gateway.pangolin.net/protocol"]; ok {
		if proto == "tcp" || proto == "udp" {
			protocol = proto
		}
	}

	// Create resource via Integration API
	// Endpoint: PUT /org/{orgId}/resource
	resourceData := map[string]interface{}{
		"name":          fmt.Sprintf("%s-%s", route.Namespace, route.Name),
		"subdomain":     subdomain,
		"http":          false, // gRPC/TCP/UDP is not HTTP
		"protocol":      protocol,
		"domainId":      domainID,
		"stickySession": false,
	}

	respData, err := r.PangolinClient.CreateResource(ctx, resourceData)
	if err != nil {
		return "", fmt.Errorf("failed to create resource: %w", err)
	}

	// Extract resource ID from response
	resourceID := fmt.Sprintf("%v", respData["resourceId"])
	if resourceID == "" {
		return "", fmt.Errorf("no resourceId in response")
	}

	log.Info("Created Pangolin resource", "resourceID", resourceID, "subdomain", subdomain, "protocol", protocol)
	return resourceID, nil
}

// updateRouteStatus updates the GRPCRoute status with conflict retry.
func (r *GRPCRouteReconciler) updateRouteStatus(ctx context.Context, route *gatewayv1.GRPCRoute, accepted bool, reason, message string) {
	if len(route.Spec.ParentRefs) == 0 {
		return
	}
	key := client.ObjectKeyFromObject(route)
	parentRef := route.Spec.ParentRefs[0]
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &gatewayv1.GRPCRoute{}
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
		newParent := gatewayv1.RouteParentStatus{
			ParentRef:      parentRef,
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
	}); err != nil {
		r.Log.Error(err, "Failed to update GRPCRoute status")
	}
}

// SetupWithManager sets up the controller with the Manager
func (r *GRPCRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("grpcroute").
		For(&gatewayv1.GRPCRoute{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.Config.MaxConcurrentReconciles,
			RateLimiter:             buildRateLimiter(r.Config),
		}).
		Complete(r)
}
